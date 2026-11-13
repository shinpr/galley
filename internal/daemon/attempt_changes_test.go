package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestRunOnceSendsAttemptChangedFilesToSupervisor(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	promptPath, schemaPath := writeDaemonPromptFiles(t)
	claudeBin := writeFakeClaude(t, "echo change > daemon-output.txt\necho '{\"status\":\"completed\",\"summary\":\"done\",\"files_modified\":[\"daemon-output.txt\"],\"acceptance_criteria\":[{\"id\":\"AC1\",\"status\":\"satisfied\",\"evidence\":[\"diff\"],\"notes\":\"done\"}],\"verification\":[],\"scope_expansions\":[],\"decisions\":[],\"risks\":[]}'\n")
	taskPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, taskPath, repo)

	if err := runTestDaemon(context.Background(), Options{
		Root:               root,
		SystemPromptFile:   promptPath,
		JSONSchemaFile:     schemaPath,
		Supervisor:         "claude",
		ClaudeBin:          claudeBin,
		Once:               true,
		MaxConcurrentTasks: 1,
	}); err != nil {
		t.Fatal(err)
	}

	var request struct {
		Evidence struct {
			AttemptChangedFiles []string `json:"attempt_changed_files"`
		} `json:"evidence"`
	}
	if err := json.Unmarshal(mustReadSingleGlob(t, filepath.Join(root, "runs", "*", "attempt-1", "*", "claude_supervisor_request.json")), &request); err != nil {
		t.Fatal(err)
	}
	if want := []string{"daemon-output.txt"}; !slices.Equal(request.Evidence.AttemptChangedFiles, want) {
		t.Fatalf("attempt_changed_files = %v, want %v", request.Evidence.AttemptChangedFiles, want)
	}
}
