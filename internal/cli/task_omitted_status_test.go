package cli

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func writeMinimalOmittedStatusTaskYAML(t *testing.T, id string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "task.yaml")
	body := strings.Join([]string{
		`id: ` + strconv.Quote(id),
		`goal: "Omitted status display."`,
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
