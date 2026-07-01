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
// (`supervisor.provider`) the current Task schema does not declare. Runtime
// loading ignores that field and decodes the known task fields.
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

func TestLoadDecodesUnknownNestedField(t *testing.T) {
	t.Parallel()
	path := writeSchemaIncompatibleTaskYAML(t, t.TempDir(), "task.yaml")
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ID != "task-schema-incompatible-1" || loaded.Status != "failed" {
		t.Fatalf("loaded fields: %#v", loaded)
	}
}

func TestArchiveUnknownFieldUsesCurrentSchemaPath(t *testing.T) {
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
	if result.Mode != "current_schema" {
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
	if !lenientArchiveStatusIs(body, "archived") {
		t.Fatalf("archived YAML missing status=archived: %q", body)
	}
	if lenientArchiveKeyHasValue(body, "provider", "unknown-supervisor") {
		t.Fatalf("unknown field provider should be ignored by struct round-trip: %q", body)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("source path should be moved, err=%v", err)
	}
}

func TestArchiveUnknownFieldStillEnforcesOpenPRCheck(t *testing.T) {
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

	_, err = Archive(path, ArchiveOptions{Reason: "schema-incompatible cleanup"})
	if err == nil {
		t.Fatal("expected open PR archive rejection")
	}
	if !strings.Contains(err.Error(), "open PR") {
		t.Fatalf("unexpected error: %v", err)
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
	// A top-level sequence rather than a mapping cannot decode as a Task, and
	// editTopLevelStatus refuses to edit it. Archive must move the file unchanged.
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

func TestRequeueUnknownFieldIgnoresField(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	failedDir := filepath.Join(root, "tasks", "failed")
	if err := os.MkdirAll(failedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := writeSchemaIncompatibleTaskYAML(t, failedDir, "task.yaml")
	result, err := Requeue(path, RequeueOptions{Root: root})
	if err != nil {
		t.Fatalf("requeue failed: %v", err)
	}
	data, err := os.ReadFile(result.To)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "provider:") {
		t.Fatalf("unknown field should be ignored in requeued struct output: %s", data)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("source path should be moved, err=%v", err)
	}
}
