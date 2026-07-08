package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSchemaIncompatibleCLITaskYAML writes a task YAML containing an unknown
// nested field (`supervisor.provider`). Runtime task loading ignores it and
// decodes the known fields.
func writeSchemaIncompatibleCLITaskYAML(t *testing.T, path string) {
	t.Helper()
	body := `id: "task-schema-incompatible-cli"
mode: "afk"
status: "failed"
goal: "Schema-incompatible CLI fixture."
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
  branch: "agent/task-schema-incompatible-cli"
  path: "../repo.worktrees/task-schema-incompatible-cli"
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
}

// writeUnreadableTaskYAML writes a file that fails even lenient YAML
// decoding because the top-level shape is incompatible with a Task struct
// (a top-level scalar). Load errors with a decode error so the CLI
// must record a non-fatal decode-error entry instead of aborting.
func writeUnreadableTaskYAML(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("just-a-string\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// writeTypeMismatchTaskYAML writes a task YAML that is genuinely undecodable
// because a KNOWN field carries the wrong YAML type: acceptance_criteria is a
// sequence of criteria, but here it is a scalar string. Unlike an unknown field
// (silently tolerated) or a top-level scalar (fails the whole-document shape),
// this exercises the decoder's per-field type checking, which is the path that
// distinguishes "skip this unreadable sibling and keep going" from "tolerate and
// decode". task.Load surfaces it as a decode error.
func writeTypeMismatchTaskYAML(t *testing.T, path string) {
	t.Helper()
	body := `id: "task-type-mismatch-cli"
mode: "afk"
status: "failed"
goal: "Type-mismatch CLI fixture."
acceptance_criteria: "this should be a list of criteria, not a scalar"
scope:
  cwd: "/tmp"
  allowed_paths:
    - "."
  forbidden_paths: []
  permission: "edit"
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestTaskListSkipsUndecodableKnownFieldSiblingAndProcessesReadableTask closes
// the seam where an unknown field is silently tolerated but a genuine decode
// failure on a known field is not exercised. A type-mismatch sibling must not
// derail the tolerant scan: it is surfaced as its own decode-error entry while a
// readable sibling in the same directory is still listed. The CLI surfaces
// undecodable tasks as a decode_error row (text) / DecodeError field (JSON) on
// its normal output rather than as a separate stderr line, so the assertions
// track that observable contract, and the command must still exit zero.
func TestTaskListSkipsUndecodableKnownFieldSiblingAndProcessesReadableTask(t *testing.T) {
	root := t.TempDir()
	failedDir := filepath.Join(root, "tasks", "failed")
	if err := os.MkdirAll(failedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Readable sibling that must still be processed.
	validData, err := os.ReadFile(writeCLITaskYAML(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(failedDir, "valid.yaml"), validData, 0o600); err != nil {
		t.Fatal(err)
	}
	// Undecodable sibling: type mismatch on a known field.
	writeTypeMismatchTaskYAML(t, filepath.Join(failedDir, "type-mismatch.yaml"))

	stdout, stderr, err := executeCommand("task", "list", "--root", root)
	if err != nil {
		t.Fatalf("task list must tolerate an undecodable known-field sibling: %v\nstderr=%q", err, stderr)
	}
	// The readable sibling is still processed.
	if !strings.Contains(stdout, "task-cli-test") {
		t.Fatalf("readable sibling missing from listing: %q", stdout)
	}
	// The undecodable sibling is surfaced as a decode-error entry, not silently
	// dropped and not fatal.
	if !strings.Contains(stdout, "decode_error") || !strings.Contains(stdout, "type-mismatch.yaml") {
		t.Fatalf("undecodable known-field sibling must render a decode-error entry: %q", stdout)
	}

	// JSON output must carry a non-empty DecodeError on the mismatch entry while
	// still emitting the readable sibling.
	jsonStdout, _, err := executeCommand("task", "list", "--root", root, "--output", "json")
	if err != nil {
		t.Fatal(err)
	}
	var items []taskListItem
	if err := json.Unmarshal([]byte(jsonStdout), &items); err != nil {
		t.Fatalf("parse json: %v\n%s", err, jsonStdout)
	}
	var sawValid, sawMismatch bool
	for _, item := range items {
		if item.ID == "task-cli-test" {
			sawValid = true
		}
		if item.DecodeError != "" && strings.HasSuffix(item.File, "type-mismatch.yaml") {
			sawMismatch = true
		}
	}
	if !sawValid || !sawMismatch {
		t.Fatalf("json scan missed entries: valid=%v mismatch=%v\n%s", sawValid, sawMismatch, jsonStdout)
	}
}

func TestTaskListSurfacesUnknownFieldAndUnreadableEntries(t *testing.T) {
	root := t.TempDir()
	// Valid task under tasks/failed.
	validTaskPath := writeCLITaskYAML(t)
	failedDir := filepath.Join(root, "tasks", "failed")
	if err := os.MkdirAll(failedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	validData, err := os.ReadFile(validTaskPath)
	if err != nil {
		t.Fatal(err)
	}
	validDest := filepath.Join(failedDir, "valid.yaml")
	if err := os.WriteFile(validDest, validData, 0o600); err != nil {
		t.Fatal(err)
	}
	// Task with an unknown nested field.
	incompatiblePath := filepath.Join(failedDir, "schema-incompatible.yaml")
	writeSchemaIncompatibleCLITaskYAML(t, incompatiblePath)
	// Unreadable task that cannot decode as a Task.
	unreadablePath := filepath.Join(failedDir, "unreadable.yaml")
	writeUnreadableTaskYAML(t, unreadablePath)

	stdout, stderr, err := executeCommand("task", "list", "--root", root)
	if err != nil {
		t.Fatalf("task list must not fail when mixed entries are present: %v\nstderr=%q", err, stderr)
	}
	if !strings.Contains(stdout, "task-cli-test") {
		t.Fatalf("valid task missing from listing: %q", stdout)
	}
	if !strings.Contains(stdout, "task-schema-incompatible-cli") {
		t.Fatalf("unknown-field task must render best-effort entry: %q", stdout)
	}
	if !strings.Contains(stdout, "decode_error") || !strings.Contains(stdout, "unreadable.yaml") {
		t.Fatalf("unreadable task must render a decode-error entry: %q", stdout)
	}

	// JSON output exposes DecodeError on the unreadable entry.
	jsonStdout, _, err := executeCommand("task", "list", "--root", root, "--output", "json")
	if err != nil {
		t.Fatal(err)
	}
	var items []taskListItem
	if err := json.Unmarshal([]byte(jsonStdout), &items); err != nil {
		t.Fatalf("parse json: %v\n%s", err, jsonStdout)
	}
	var sawValid, sawIncompatible, sawUnreadable bool
	for _, item := range items {
		switch item.ID {
		case "task-cli-test":
			sawValid = true
		case "task-schema-incompatible-cli":
			sawIncompatible = true
		}
		if item.DecodeError != "" && strings.HasSuffix(item.File, "unreadable.yaml") {
			sawUnreadable = true
		}
	}
	if !sawValid || !sawIncompatible || !sawUnreadable {
		t.Fatalf("json scan missed entries valid=%v schema_incompatible=%v unreadable=%v: %s", sawValid, sawIncompatible, sawUnreadable, jsonStdout)
	}
}

func TestTaskShowUnknownFieldTaskByIDSucceeds(t *testing.T) {
	root := t.TempDir()
	failedDir := filepath.Join(root, "tasks", "failed")
	if err := os.MkdirAll(failedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	incompatiblePath := filepath.Join(failedDir, "schema-incompatible.yaml")
	writeSchemaIncompatibleCLITaskYAML(t, incompatiblePath)

	stdout, stderr, err := executeCommand("task", "show", "--root", root, "task-schema-incompatible-cli")
	if err != nil {
		t.Fatalf("task show should ignore unknown fields: %v\nstderr=%q", err, stderr)
	}
	if !strings.Contains(stdout, "id: task-schema-incompatible-cli") {
		t.Fatalf("unexpected stdout: %q", stdout)
	}
}

// TestTaskShowSkipsUnreadableEntriesDuringIDLookup covers the contract that
// findTaskByID must not be derailed by a sibling unreadable file: the lookup
// for a readable ID must still succeed even when an unreadable historical
// file lives next to the valid one.
func TestTaskShowSkipsUnreadableEntriesDuringIDLookup(t *testing.T) {
	root := t.TempDir()
	failedDir := filepath.Join(root, "tasks", "failed")
	if err := os.MkdirAll(failedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Sibling unreadable file.
	writeUnreadableTaskYAML(t, filepath.Join(failedDir, "unreadable.yaml"))
	// Sibling file with an unknown field and a different ID.
	writeSchemaIncompatibleCLITaskYAML(t, filepath.Join(failedDir, "schema-incompatible.yaml"))
	// Valid task under the same directory.
	validTaskPath := writeCLITaskYAML(t)
	validData, err := os.ReadFile(validTaskPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(failedDir, "valid.yaml"), validData, 0o600); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, err := executeCommand("task", "show", "--root", root, "task-cli-test")
	if err != nil {
		t.Fatalf("show by ID must succeed even when siblings are unreadable: %v\nstderr=%q", err, stderr)
	}
	if !strings.Contains(stdout, "id: task-cli-test") {
		t.Fatalf("unexpected stdout: %q", stdout)
	}
}

func TestTaskArchiveUnknownFieldUsesCurrentSchemaMode(t *testing.T) {
	root := t.TempDir()
	doneDir := filepath.Join(root, "tasks", "done")
	if err := os.MkdirAll(doneDir, 0o755); err != nil {
		t.Fatal(err)
	}
	incompatiblePath := filepath.Join(doneDir, "schema-incompatible.yaml")
	writeSchemaIncompatibleCLITaskYAML(t, incompatiblePath)

	stdout, stderr, err := executeCommand("task", "archive", "--reason", "lenient cleanup", incompatiblePath)
	if err != nil {
		t.Fatalf("archive must succeed for unknown-field task: %v", err)
	}
	if strings.Contains(stdout, "mode: lenient_status_edit") {
		t.Fatalf("unknown-field archive should use current schema mode: stdout=%q", stdout)
	}
	if stderr != "" {
		t.Fatalf("unknown-field archive should not warn: stderr=%q", stderr)
	}
	archived := filepath.Join(root, "tasks", "archived", "schema-incompatible.yaml")
	if _, err := os.Stat(archived); err != nil {
		t.Fatalf("archived file missing: %v", err)
	}
}

func TestTaskArchiveJSONUsesCurrentSchemaForUnknownField(t *testing.T) {
	root := t.TempDir()
	doneDir := filepath.Join(root, "tasks", "done")
	if err := os.MkdirAll(doneDir, 0o755); err != nil {
		t.Fatal(err)
	}
	incompatiblePath := filepath.Join(doneDir, "schema-incompatible.yaml")
	writeSchemaIncompatibleCLITaskYAML(t, incompatiblePath)

	stdout, _, err := executeCommand("task", "archive", "--output", "json", incompatiblePath)
	if err != nil {
		t.Fatalf("archive json must succeed for unknown-field task: %v", err)
	}
	var payload struct {
		Mode    string `json:"mode"`
		Warning string `json:"warning"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("parse json: %v\n%s", err, stdout)
	}
	if payload.Mode != "current_schema" {
		t.Fatalf("mode got %q want current_schema", payload.Mode)
	}
	if payload.Warning != "" {
		t.Fatalf("warning got %q", payload.Warning)
	}
}

// TestTaskArchiveUnreadableFallbackUsesPathLabel covers the text fallback for
// files that cannot yield a task ID. The command should still print a useful
// archive label instead of `archived: ` with an empty identifier.
func TestTaskArchiveUnreadableFallbackUsesPathLabel(t *testing.T) {
	root := t.TempDir()
	failedDir := filepath.Join(root, "tasks", "failed")
	if err := os.MkdirAll(failedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	unreadablePath := filepath.Join(failedDir, "unreadable.yaml")
	writeUnreadableTaskYAML(t, unreadablePath)

	stdout, stderr, err := executeCommand("task", "archive", "--reason", "unreadable cleanup", unreadablePath)
	if err != nil {
		t.Fatalf("archive must succeed for unreadable task: %v", err)
	}
	archivedPath := filepath.Join(root, "tasks", "archived", "unreadable.yaml")
	if !strings.Contains(stdout, "archived: "+archivedPath) {
		t.Fatalf("text output should fall back to archived path when ID is unavailable: stdout=%q", stdout)
	}
	if !strings.Contains(stdout, "mode: move_unreadable_unchanged") {
		t.Fatalf("text output must surface unchanged unreadable archive mode: stdout=%q", stdout)
	}
	if !strings.Contains(stderr, "warning:") || !strings.Contains(stderr, "archived unchanged") {
		t.Fatalf("text output must surface ArchiveResult.Warning on stderr: stderr=%q", stderr)
	}
}
