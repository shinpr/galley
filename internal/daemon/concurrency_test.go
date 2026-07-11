package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shinpr/galley/internal/task"
)

func TestRunProcessesTasksConcurrentlyAcrossRepos(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	promptPath, schemaPath := writeDaemonPromptFiles(t)
	claudeBin := writeFakeClaude(t, "echo change > daemon-output.txt\necho '{\"status\":\"completed\",\"summary\":\"done\",\"files_modified\":[\"daemon-output.txt\"],\"acceptance_criteria\":[{\"id\":\"AC1\",\"status\":\"satisfied\",\"evidence\":[\"diff\"],\"notes\":\"done\"}],\"verification\":[],\"scope_expansions\":[],\"decisions\":[],\"risks\":[]}'\n")
	queued := filepath.Join(root, "tasks", "queued")
	if err := os.MkdirAll(queued, 0o755); err != nil {
		t.Fatal(err)
	}

	names := []string{"task-a", "task-b"}
	for _, name := range names {
		repo := initDaemonGitRepo(t)
		p := filepath.Join(queued, name+".yaml")
		writeDaemonTask(t, p, repo)
		setTaskID(t, p, name)
		setLoopBudget(t, p, 2)
	}

	if err := runTestDaemon(context.Background(), Options{
		Root:               root,
		SystemPromptFile:   promptPath,
		JSONSchemaFile:     schemaPath,
		Once:               true,
		MaxConcurrentTasks: 2,
		Supervisor:         "claude",
		ClaudeBin:          claudeBin,
	}); err != nil {
		t.Fatal(err)
	}

	for _, name := range names {
		donePath := filepath.Join(root, "tasks", "done", name+".yaml")
		dt, err := task.Load(donePath)
		if err != nil {
			t.Fatalf("task %s did not reach done: %v", name, err)
		}
		if dt.Status != "accepted" {
			t.Fatalf("task %s status = %q, want accepted", name, dt.Status)
		}
	}
	assertGlobCount(t, filepath.Join(root, "runs", "*", "task.yaml"), 2)
}

func setTaskID(t *testing.T, path, id string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	updated := strings.Replace(string(data), `id: "task-daemon-test"`, `id: "`+id+`"`, 1)
	if updated == string(data) {
		t.Fatalf("task id line not found in %s", path)
	}
	if err := os.WriteFile(path, []byte(updated), 0o600); err != nil {
		t.Fatal(err)
	}
}
