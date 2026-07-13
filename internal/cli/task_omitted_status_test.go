package cli

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	taskpkg "github.com/shinpr/galley/internal/task"
)

// writeMinimalOmittedStatusTaskYAML writes an author-facing draft that omits
// status (and other fixed AFK defaults) so display and queue eligibility must
// resolve draft via ApplyDefaults.
func writeMinimalOmittedStatusTaskYAML(t *testing.T, id string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "task.yaml")
	body := strings.Join([]string{
		`id: ` + strconv.Quote(id),
		`goal: "Omitted status display and queue persistence."`,
		`acceptance_criteria:`,
		`  - id: "AC1"`,
		`    text: "observable"`,
		`    verification: "test"`,
		`    status: "pending"`,
		`scope:`,
		`  cwd: ` + strconv.Quote(dir),
		`  allowed_paths: ["."]`,
		`  forbidden_paths: [".env"]`,
		`  permission: "edit"`,
		`execution_policy:`,
		`  loop_budget: 10`,
		`  timeout_ms: 1000`,
		`worktree:`,
		`  branch: "agent/` + id + `"`,
		`  path: "../repo.worktrees/` + id + `"`,
		`decisions: []`,
		`risks: []`,
		``,
	}, "\n")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Criterion-level status is expected; top-level task status must stay omitted.
	if strings.Contains(string(raw), "\nstatus:") || strings.HasPrefix(string(raw), "status:") {
		t.Fatalf("fixture must omit top-level status, got:\n%s", raw)
	}
	return path
}

func TestTaskShowAndListDisplayOmittedStatusAsDraft(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	const id = "task-omitted-status-display"
	src := writeMinimalOmittedStatusTaskYAML(t, id)
	draftPath := filepath.Join(root, "tasks", "draft", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(draftPath), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(draftPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	showOut, showErr, err := executeCommand("task", "show", "--root", root, id)
	if err != nil {
		t.Fatalf("task show: %v\nstderr=%q", err, showErr)
	}
	if !strings.Contains(showOut, "status: draft") {
		t.Fatalf("task show must display omitted status as draft:\n%s", showOut)
	}

	listOut, listErr, err := executeCommand("task", "list", "--root", root)
	if err != nil {
		t.Fatalf("task list: %v\nstderr=%q", err, listErr)
	}
	if !strings.Contains(listOut, "draft\tdraft\t"+id) {
		t.Fatalf("task list must display omitted status as draft:\n%s", listOut)
	}
}

func TestTaskQueuePersistsQueuedStatusFromOmittedStatusDraft(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	const id = "task-omitted-status-queue"
	src := writeMinimalOmittedStatusTaskYAML(t, id)

	stdout, stderr, err := executeCommand("task", "queue", "--root", root, "--reason", "persist queued status", src)
	if err != nil {
		t.Fatalf("task queue: %v\nstdout=%q stderr=%q", err, stdout, stderr)
	}
	if !strings.Contains(stdout, "queued: "+id) {
		t.Fatalf("queue output missing queued id: %q", stdout)
	}

	// Queue may rename by source basename; also accept the standard task.yaml path.
	queuedPath := filepath.Join(root, "tasks", "queued", filepath.Base(src))
	if _, err := os.Stat(queuedPath); err != nil {
		entries, readErr := os.ReadDir(filepath.Join(root, "tasks", "queued"))
		if readErr != nil {
			t.Fatalf("queued dir: %v (stat %s: %v)", readErr, queuedPath, err)
		}
		if len(entries) != 1 {
			t.Fatalf("expected one queued file, got %#v (stat %s: %v)", entries, queuedPath, err)
		}
		queuedPath = filepath.Join(root, "tasks", "queued", entries[0].Name())
	}

	loaded, err := taskpkg.Load(queuedPath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != taskpkg.StatusQueued {
		t.Fatalf("queued task status got %q, want %q", loaded.Status, taskpkg.StatusQueued)
	}
	raw, err := os.ReadFile(queuedPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "status: queued") && !strings.Contains(string(raw), `status: "queued"`) {
		t.Fatalf("queued YAML must persist status queued:\n%s", raw)
	}
}
