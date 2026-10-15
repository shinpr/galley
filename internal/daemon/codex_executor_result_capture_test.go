package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shinpr/galley/internal/runartifact"
	"github.com/shinpr/galley/internal/runner"
	"github.com/shinpr/galley/internal/task"
)

func TestCodexExecutorLastMessageJSONReachesExecutorResult(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	promptPath, schemaPath := writeDaemonPromptFiles(t)

	executorResult := `{"status":"completed","summary":"codex last-message summary","files_modified":["daemon-output.txt"],"acceptance_criteria":[{"id":"AC1","status":"satisfied","evidence":["diff"],"notes":"done"}],"verification":[],"scope_expansions":[],"decisions":[],"risks":[]}`
	// The fake executor writes the result to --output-last-message and emits a
	// non-result line on stdout so the test only passes when the daemon parses
	// the last-message file rather than relying on stdout JSONL.
	claudeBin := writeFakeClaude(t, "exit 1\n")
	codexBin := writeFakeCommand(t, "codex", `lastmsg=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --output-last-message)
      lastmsg="$2"
      shift 2
      ;;
    *)
      shift
      ;;
  esac
done
cat >/dev/null
echo change > daemon-output.txt
if [ -n "$lastmsg" ]; then
  printf '%s' '`+executorResult+`' > "$lastmsg"
fi
printf '%s\n' '{"event":"unrelated"}'
printf '%s\n' '{"type":"turn.completed","usage":{}}'
`)

	taskPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, taskPath, repo)
	loaded, err := task.Load(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Executor.CLI = "codex"
	if err := task.Save(taskPath, loaded); err != nil {
		t.Fatal(err)
	}

	if err := runTestDaemon(context.Background(), Options{
		Root:               root,
		SystemPromptFile:   promptPath,
		JSONSchemaFile:     schemaPath,
		Once:               true,
		MaxConcurrentTasks: 1,
		Supervisor:         "claude",
		ClaudeBin:          claudeBin,
		CodexBin:           codexBin,
	}); err != nil {
		t.Fatal(err)
	}

	data := mustReadSingleGlob(t, filepath.Join(root, "runs", "*", "attempt-1", runartifact.ExecutorResultFilename))
	var got runner.ExecutorResult
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decode executor_result.json: %v", err)
	}
	if got.Status != "completed" {
		t.Fatalf("status got %q, want completed (parsed from last-message file)", got.Status)
	}
	if !strings.Contains(got.Summary, "codex last-message summary") {
		t.Fatalf("summary missing last-message body; got %q", got.Summary)
	}
}
