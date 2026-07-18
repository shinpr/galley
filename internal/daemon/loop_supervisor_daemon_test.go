package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shinpr/galley/internal/runner"
	"github.com/shinpr/galley/internal/supervisor"
	"github.com/shinpr/galley/internal/task"
)

func newDaemonSupervisorTask(t *testing.T) (root, taskPath string) {
	t.Helper()
	root = filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	taskPath = filepath.Join(root, "tasks", "queued", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, taskPath, repo)
	return root, taskPath
}

func TestDaemonSupervisorStallFailsAfterSingleInvocation(t *testing.T) {
	root, taskPath := newDaemonSupervisorTask(t)
	promptPath, schemaPath := writeDaemonPromptFiles(t)
	claudeBin := writeFakeClaude(t, "echo change > daemon-output.txt\necho '{\"status\":\"completed\",\"summary\":\"done\",\"files_modified\":[\"daemon-output.txt\"],\"acceptance_criteria\":[{\"id\":\"AC1\",\"status\":\"satisfied\",\"evidence\":[\"diff\"],\"notes\":\"done\"}],\"verification\":[],\"scope_expansions\":[],\"decisions\":[],\"risks\":[]}'\n")
	setLoopBudget(t, taskPath, 3)
	calls := 0
	stallingSupervisor := func(_ context.Context, _ Options, _ supervisor.Evidence, _, _ string) (supervisor.Verdict, error) {
		calls++
		return supervisor.Verdict{}, &runner.CommandError{Kind: runner.CommandErrorIdleTimeout, Err: errors.New("no output")}
	}

	err := runTestDaemon(context.Background(), Options{
		Root: root, SystemPromptFile: promptPath, JSONSchemaFile: schemaPath,
		Once: true, MaxConcurrentTasks: 1, Supervisor: "codex",
		ClaudeBin:    claudeBin,
		dependencies: &daemonDependencies{supervisorRunner: stallingSupervisor},
	})
	if err == nil {
		t.Fatal("expected supervisor idle timeout")
	}
	if calls != 1 {
		t.Fatalf("supervisor calls got %d, want 1", calls)
	}

	failed, loadErr := task.Load(filepath.Join(root, "tasks", "failed", "task.yaml"))
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if failed.Status != "needs_supervisor_review" || len(failed.Attempts) != 1 || failed.Attempts[0].Error == nil {
		t.Fatalf("failed task = %#v", failed)
	}
	if failed.Attempts[0].Error.Kind != "supervisor_idle_timeout" {
		t.Fatalf("error kind got %q, want supervisor_idle_timeout", failed.Attempts[0].Error.Kind)
	}
	for _, want := range []string{"supervisor=codex", "idle_timeout="} {
		if !strings.Contains(failed.Attempts[0].Error.Message, want) {
			t.Fatalf("error message %q must contain %q", failed.Attempts[0].Error.Message, want)
		}
	}
	assertGlobCount(t, filepath.Join(root, "runs", "*", "attempt-1", "supervisor-try-1", "supervisor_error.json"), 1)
	assertGlobCount(t, filepath.Join(root, "runs", "*", "attempt-1", "supervisor-try-2"), 0)
	assertGlobCount(t, filepath.Join(root, "runs", "*", "attempt-2"), 0)
}

func TestGrokSupervisorStartFailureIsOwnedBySupervisorError(t *testing.T) {
	root, taskPath := newDaemonSupervisorTask(t)
	promptPath, schemaPath := writeDaemonPromptFiles(t)
	claudeBin := writeFakeClaude(t, "echo change > daemon-output.txt\necho '{\"status\":\"completed\",\"summary\":\"done\",\"files_modified\":[\"daemon-output.txt\"],\"acceptance_criteria\":[{\"id\":\"AC1\",\"status\":\"satisfied\",\"evidence\":[\"diff\"],\"notes\":\"done\"}],\"verification\":[],\"scope_expansions\":[],\"decisions\":[],\"risks\":[]}'\n")
	setLoopBudget(t, taskPath, 1)
	err := runTestDaemon(context.Background(), Options{Root: root, SystemPromptFile: promptPath, JSONSchemaFile: schemaPath, Once: true, MaxConcurrentTasks: 1, Supervisor: "grok", ClaudeBin: claudeBin, GrokBin: filepath.Join(t.TempDir(), "missing-grok")})
	if err == nil {
		t.Fatal("expected Grok supervisor start failure")
	}
	data := mustReadSingleGlob(t, filepath.Join(root, "runs", "*", "attempt-1", "supervisor-try-1", "supervisor_error.json"))
	if !strings.Contains(string(data), `"kind": "supervisor_failed"`) || !strings.Contains(string(data), "start") {
		t.Fatalf("supervisor error evidence = %s", data)
	}
	assertGlobCount(t, filepath.Join(root, "runs", "*", "attempt-1", "supervisor-try-2"), 0)
}
