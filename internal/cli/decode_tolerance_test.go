package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSchemaIncompatibleCLITaskYAML writes a task YAML containing an unknown
// nested field (`supervisor.provider`). Strict Load rejects it; lenient scans
// surface it as a best-effort entry.
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
// (a top-level scalar). LoadLenient errors with a decode error so the CLI
// must record a non-fatal decode-error entry instead of aborting.
func writeUnreadableTaskYAML(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("just-a-string\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestTaskListSurfacesStrictDecodeIncompatibleAndUnreadableEntries covers the AC requiring
// `galley task list` to mix valid, strict-decode-incompatible, and unreadable
// task files in one command without failing.
func TestTaskListSurfacesStrictDecodeIncompatibleAndUnreadableEntries(t *testing.T) {
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
	// Strict-decode-incompatible task with an unknown nested field.
	incompatiblePath := filepath.Join(failedDir, "schema-incompatible.yaml")
	writeSchemaIncompatibleCLITaskYAML(t, incompatiblePath)
	// Unreadable task that even LoadLenient can't decode as a Task.
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
		t.Fatalf("strict-decode-incompatible task must render best-effort entry: %q", stdout)
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

// TestTaskShowStrictDecodeIncompatibleTaskByIDFails covers `galley task show <ID>` against a
// strict-decode-incompatible task: the ID resolves via the tolerant scan, but the strict Load
// inside `task show` correctly rejects it. The command must surface a
// readable error rather than treat the strict-decode-incompatible task as a current-schema one.
func TestTaskShowStrictDecodeIncompatibleTaskByIDFails(t *testing.T) {
	root := t.TempDir()
	failedDir := filepath.Join(root, "tasks", "failed")
	if err := os.MkdirAll(failedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	incompatiblePath := filepath.Join(failedDir, "schema-incompatible.yaml")
	writeSchemaIncompatibleCLITaskYAML(t, incompatiblePath)

	_, _, err := executeCommand("task", "show", "--root", root, "task-schema-incompatible-cli")
	if err == nil {
		t.Fatal("expected task show to surface strict decode error for strict-decode-incompatible task")
	}
	if !strings.Contains(err.Error(), "provider") {
		t.Fatalf("error should reference the unknown field: %v", err)
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
	// Sibling schema-incompatible file (decodes leniently with a different ID).
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

// TestTaskArchiveStrictDecodeIncompatibleFallbackEmitsWarning covers AC: `galley task archive`
// text output must surface ArchiveResult.Warning so operators see why the
// strict-decode-incompatible file was archived through the safe fallback path instead of the
// strict current-schema path.
func TestTaskArchiveStrictDecodeIncompatibleFallbackEmitsWarning(t *testing.T) {
	root := t.TempDir()
	doneDir := filepath.Join(root, "tasks", "done")
	if err := os.MkdirAll(doneDir, 0o755); err != nil {
		t.Fatal(err)
	}
	incompatiblePath := filepath.Join(doneDir, "schema-incompatible.yaml")
	writeSchemaIncompatibleCLITaskYAML(t, incompatiblePath)

	stdout, stderr, err := executeCommand("task", "archive", "--reason", "lenient cleanup", incompatiblePath)
	if err != nil {
		t.Fatalf("archive must succeed for strict-decode-incompatible task: %v", err)
	}
	if !strings.Contains(stdout, "mode: lenient_status_edit") {
		t.Fatalf("text output must surface lenient archive mode: stdout=%q", stdout)
	}
	if !strings.Contains(stderr, "warning:") || !strings.Contains(stderr, "strict load failed") {
		t.Fatalf("text output must surface ArchiveResult.Warning on stderr: stderr=%q", stderr)
	}
	archived := filepath.Join(root, "tasks", "archived", "schema-incompatible.yaml")
	if _, err := os.Stat(archived); err != nil {
		t.Fatalf("archived file missing: %v", err)
	}
}

// TestTaskArchiveJSONExposesModeAndWarning ensures the structured channel
// agrees with the text branch: JSON consumers also see Mode and Warning so
// automation (CI, the daemon supervisor) can react without parsing text.
func TestTaskArchiveJSONExposesModeAndWarning(t *testing.T) {
	root := t.TempDir()
	doneDir := filepath.Join(root, "tasks", "done")
	if err := os.MkdirAll(doneDir, 0o755); err != nil {
		t.Fatal(err)
	}
	incompatiblePath := filepath.Join(doneDir, "schema-incompatible.yaml")
	writeSchemaIncompatibleCLITaskYAML(t, incompatiblePath)

	stdout, _, err := executeCommand("task", "archive", "--output", "json", incompatiblePath)
	if err != nil {
		t.Fatalf("archive json must succeed for strict-decode-incompatible task: %v", err)
	}
	var payload struct {
		Mode    string `json:"mode"`
		Warning string `json:"warning"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("parse json: %v\n%s", err, stdout)
	}
	if payload.Mode != "lenient_status_edit" {
		t.Fatalf("mode got %q want lenient_status_edit", payload.Mode)
	}
	if !strings.Contains(payload.Warning, "strict load failed") {
		t.Fatalf("warning missing: %q", payload.Warning)
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
