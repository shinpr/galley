package task

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// lenientArchiveStatusIs reports whether the top-level `status` field in the
// given task YAML body parses to want. It tolerates any lexical formatting
// (quoting style, indentation) the YAML round-trip may produce, since the
// observable contract is the decoded value rather than the byte layout.
func lenientArchiveStatusIs(body, want string) bool {
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(body), &doc); err != nil {
		return false
	}
	got, _ := doc["status"].(string)
	return got == want
}

// lenientArchiveKeyHasValue reports whether some key path in the YAML body
// has the given string scalar value. It walks the tree to find the first
// occurrence of key, which is enough for fixtures here (the unknown field
// only appears once).
func lenientArchiveKeyHasValue(body, key, want string) bool {
	var doc any
	if err := yaml.Unmarshal([]byte(body), &doc); err != nil {
		return false
	}
	return findScalarValue(doc, key, want)
}

func findScalarValue(node any, key, want string) bool {
	switch v := node.(type) {
	case map[string]any:
		if got, ok := v[key].(string); ok && got == want {
			return true
		}
		for _, child := range v {
			if findScalarValue(child, key, want) {
				return true
			}
		}
	case []any:
		for _, child := range v {
			if findScalarValue(child, key, want) {
				return true
			}
		}
	}
	return false
}

// writeSchemaIncompatibleTaskYAML writes a task YAML file that contains a field
// (`supervisor.provider`) the current Task schema does not declare. It
// preserves the rest of the current-schema fields so the only reason strict
// Load fails is the unknown nested field.
func writeSchemaIncompatibleTaskYAML(t *testing.T, dir, baseName string) string {
	t.Helper()
	path := filepath.Join(dir, baseName)
	body := `id: "task-schema-incompatible-1"
mode: "afk"
status: "failed"
goal: "Schema-incompatible task fixture."
acceptance_criteria:
  - id: "AC1"
    text: "Loads."
    verification: "go test ./..."
    status: "pending"
scope:
  cwd: "/tmp"
  allowed_paths:
    - "."
  forbidden_paths: []
  permission: "edit"
execution_policy:
  loop_budget: 3
  timeout_ms: 600000
  afk_decision_policy: "choose-smallest-reversible"
  stop_on_destructive_operation: true
  stop_on_missing_secret: false
  stop_on_external_service_unavailable: false
worktree:
  enabled: true
  branch: "agent/task-schema-incompatible-1"
  path: "../repo.worktrees/task-schema-incompatible-1"
supervisor:
  review_iterations: 0
  provider: "unknown-supervisor"
executor:
  cli: "claude"
  effort: "high"
  prompt_profile: "codexized-claude-executor-v1"
  prompt_mode: "replace"
decisions: []
risks: []
attempts: []
verification:
  commands: []
pr:
  url: ""
  status: ""
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestLoadRejectsStrictDecodeIncompatibleUnknownNestedField guards AC4 strictness: active task
// intake must still reject strict-decode-incompatible unknown nested fields.
func TestLoadRejectsStrictDecodeIncompatibleUnknownNestedField(t *testing.T) {
	t.Parallel()
	path := writeSchemaIncompatibleTaskYAML(t, t.TempDir(), "task.yaml")
	if _, err := Load(path); err == nil {
		t.Fatal("expected strict Load to reject strict-decode-incompatible unknown nested field")
	}
}

// TestLoadLenientDecodesStrictDecodeIncompatibleUnknownNestedField covers the tolerant read
// API used by scans (list/show/helper sweeps).
func TestLoadLenientDecodesStrictDecodeIncompatibleUnknownNestedField(t *testing.T) {
	t.Parallel()
	path := writeSchemaIncompatibleTaskYAML(t, t.TempDir(), "task.yaml")
	loaded, err := LoadLenient(path)
	if err != nil {
		t.Fatalf("lenient load failed: %v", err)
	}
	if loaded.ID != "task-schema-incompatible-1" || loaded.Status != "failed" {
		t.Fatalf("lenient load fields: %#v", loaded)
	}
}

// TestArchiveStrictDecodeIncompatibleUnknownFieldUsesStatusEdit covers AC6 path 2: editable
// top-level status, unknown fields retained across YAML reserialization.
func TestArchiveStrictDecodeIncompatibleUnknownFieldUsesStatusEdit(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	doneDir := filepath.Join(root, "tasks", "done")
	if err := os.MkdirAll(doneDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := writeSchemaIncompatibleTaskYAML(t, doneDir, "task.yaml")
	result, err := Archive(path, ArchiveOptions{Reason: "schema-incompatible cleanup"})
	if err != nil {
		t.Fatalf("archive failed: %v", err)
	}
	if result.Mode != "lenient_status_edit" {
		t.Fatalf("mode got %q", result.Mode)
	}
	archivedPath := filepath.Join(root, "tasks", "archived", "task.yaml")
	if result.To != archivedPath {
		t.Fatalf("to got %q", result.To)
	}
	data, err := os.ReadFile(archivedPath)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	// Verify the unknown field "provider" was retained by checking
	// the value appears under a "provider:" key. Exact YAML lexical
	// formatting (quoting style, indentation) is not asserted: yaml.Node
	// round-tripping may normalize scalar style and indentation, but the
	// observable contract is that no unknown field is lost.
	if !lenientArchiveStatusIs(body, "archived") {
		t.Fatalf("archived YAML missing status=archived: %q", body)
	}
	if !lenientArchiveKeyHasValue(body, "provider", "unknown-supervisor") {
		t.Fatalf("unknown field provider must be preserved: %q", body)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("source path should be moved, err=%v", err)
	}
}

func TestArchiveStrictDecodeIncompatibleSkipsOpenPRCheck(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	doneDir := filepath.Join(root, "tasks", "done")
	if err := os.MkdirAll(doneDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := writeSchemaIncompatibleTaskYAML(t, doneDir, "task.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	body = strings.Replace(body, `url: ""`, `url: "https://github.com/example/repo/pull/1"`, 1)
	body = strings.Replace(body, `status: ""`, `status: "open"`, 1)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := Archive(path, ArchiveOptions{Reason: "schema-incompatible cleanup"})
	if err != nil {
		t.Fatalf("strict-decode-incompatible archive should skip non-authoritative PR open check: %v", err)
	}
	if result.Mode != "lenient_status_edit" {
		t.Fatalf("mode got %q", result.Mode)
	}
	if _, err := os.Stat(filepath.Join(root, "tasks", "archived", "task.yaml")); err != nil {
		t.Fatalf("archived task missing: %v", err)
	}
}

// TestArchiveUnreadableMovesUnchangedWhenStatusEditUnsafe covers AC6 path 3 with
// a YAML that parses but whose top-level is not a mapping.
func TestArchiveUnreadableMovesUnchangedWhenStatusEditUnsafe(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	doneDir := filepath.Join(root, "tasks", "done")
	if err := os.MkdirAll(doneDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(doneDir, "broken.yaml")
	// A top-level sequence rather than a mapping: strict Load fails, and
	// editTopLevelStatus refuses to edit it. Archive must move the file
	// unchanged.
	if err := os.WriteFile(path, []byte("- not-a-task\n- another\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := Archive(path, ArchiveOptions{})
	if err != nil {
		t.Fatalf("archive failed: %v", err)
	}
	if result.Mode != "move_unreadable_unchanged" {
		t.Fatalf("mode got %q", result.Mode)
	}
	archivedPath := filepath.Join(root, "tasks", "archived", "broken.yaml")
	data, err := os.ReadFile(archivedPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "- not-a-task\n- another\n" {
		t.Fatalf("archived bytes should round-trip unchanged, got %q", string(data))
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("source path should be moved, err=%v", err)
	}
}

// TestArchiveStrictDecodeIncompatibleRefusesDestinationConflict guards AC6's failure boundary:
// archive must still surface destination conflicts.
func TestArchiveStrictDecodeIncompatibleRefusesDestinationConflict(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	doneDir := filepath.Join(root, "tasks", "done")
	archivedDir := filepath.Join(root, "tasks", "archived")
	if err := os.MkdirAll(doneDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(archivedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := writeSchemaIncompatibleTaskYAML(t, doneDir, "task.yaml")
	if err := os.WriteFile(filepath.Join(archivedDir, "task.yaml"), []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Archive(path, ArchiveOptions{}); err == nil {
		t.Fatal("expected destination conflict error")
	}
}

// TestRequeueRejectsStrictDecodeIncompatibleTask covers AC5: requeue of a
// strict-decode-incompatible task fails clearly and leaves the file untouched.
func TestRequeueRejectsStrictDecodeIncompatibleTask(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	failedDir := filepath.Join(root, "tasks", "failed")
	if err := os.MkdirAll(failedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := writeSchemaIncompatibleTaskYAML(t, failedDir, "task.yaml")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Requeue(path, RequeueOptions{Root: root}); err == nil {
		t.Fatal("expected requeue to reject strict-decode-incompatible task")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("strict-decode-incompatible task must not be removed: %v", err)
	}
	if string(after) != string(original) {
		t.Fatalf("strict-decode-incompatible task must not be modified by requeue")
	}
	if _, err := os.Stat(filepath.Join(root, "tasks", "queued", "task.yaml")); !os.IsNotExist(err) {
		t.Fatalf("requeue must not publish a queued copy of a strict-decode-incompatible task, err=%v", err)
	}
}
