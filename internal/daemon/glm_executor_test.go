package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shinpr/galley/internal/task"
)

func loadGLMExecutorTask(t *testing.T, taskPath, repo string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, taskPath, repo)
	loaded, err := task.Load(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Executor.CLI = "glm"
	if err := task.Save(taskPath, loaded); err != nil {
		t.Fatal(err)
	}
}

func TestGLMExecutorNormalTerminalReachesSupervisor(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	promptPath, schemaPath := writeDaemonPromptFiles(t)
	validResult := `{"status":"completed","summary":"done","files_modified":["daemon-output.txt"],"acceptance_criteria":[{"id":"AC1","status":"satisfied","evidence":["diff"],"notes":"done"}],"verification":[],"scope_expansions":[],"decisions":[],"risks":[]}`
	claudeBin := writeFakeClaude(t, "echo change > daemon-output.txt\necho '"+validResult+"'\n")

	taskPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	loadGLMExecutorTask(t, taskPath, repo)

	if err := runTestDaemon(context.Background(), Options{Root: root, SystemPromptFile: promptPath, JSONSchemaFile: schemaPath, Once: true, MaxConcurrentTasks: 1, Supervisor: "claude", ClaudeBin: claudeBin, GLMAuthToken: "test-token"}); err != nil {
		t.Fatal(err)
	}

	done, err := task.Load(filepath.Join(root, "tasks", "done", "task.yaml"))
	if err != nil {
		t.Fatalf("done task missing: %v", err)
	}
	if done.Status != "accepted" {
		t.Fatalf("status = %q; want accepted", done.Status)
	}
	if len(done.Attempts) != 1 || done.Attempts[0].SupervisorVerdict != "accepted" {
		t.Fatalf("attempts = %#v; want one accepted attempt", done.Attempts)
	}
	if done.Attempts[0].Error != nil && done.Attempts[0].Error.Kind == task.AttemptKindExecutorInterrupted {
		t.Fatalf("a normal GLM terminal must not be an interruption: %#v", done.Attempts[0].Error)
	}
	assertGlobCount(t, filepath.Join(root, "runs", "*", "attempt-1", "supervisor_verdict.json"), 1)
}

func TestGLMExecutorAPIErrorInterruptionSkipsSupervisor(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	promptPath, schemaPath := writeDaemonPromptFiles(t)
	claudeBin := writeFakeClaude(t, `echo change > daemon-output.txt`+"\n"+`echo '{"type":"result","subtype":"error_during_execution","is_error":true,"session_id":"glm-sess","error":"rate limited"}'`+"\n")

	taskPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	loadGLMExecutorTask(t, taskPath, repo)
	// loop_budget > 1 proves the interruption never starts attempt 2.
	setLoopBudget(t, taskPath, 3)

	_ = runTestDaemon(context.Background(), Options{Root: root, SystemPromptFile: promptPath, JSONSchemaFile: schemaPath, Once: true, MaxConcurrentTasks: 1, Supervisor: "claude", ClaudeBin: claudeBin, GLMAuthToken: "test-token"})

	failed := mustLoadFailedTask(t, root)
	if len(failed.Attempts) != 1 {
		t.Fatalf("attempts = %d; want 1: %#v", len(failed.Attempts), failed.Attempts)
	}
	attempt := failed.Attempts[0]
	if attempt.SupervisorVerdict != task.AttemptVerdictNotReviewed {
		t.Fatalf("verdict = %q; want %q", attempt.SupervisorVerdict, task.AttemptVerdictNotReviewed)
	}
	if attempt.Error == nil || attempt.Error.Kind != task.AttemptKindExecutorInterrupted {
		t.Fatalf("attempt error = %#v; want kind %q", attempt.Error, task.AttemptKindExecutorInterrupted)
	}
	if !strings.Contains(attempt.Error.Message, "message=rate limited") || !strings.Contains(attempt.Error.Message, "session_id=glm-sess") {
		t.Fatalf("interruption must retain GLM provider detail: %q", attempt.Error.Message)
	}
	if attempt.Error.ArtifactDir == "" {
		t.Fatal("interruption must record the artifact directory task show surfaces")
	}
	foundRequeue := false
	for _, r := range failed.Risks {
		if strings.Contains(r.Mitigation, "galley task requeue") {
			foundRequeue = true
		}
	}
	if !foundRequeue {
		t.Fatalf("interruption must record requeue recovery guidance: %#v", failed.Risks)
	}
	assertGlobCount(t, filepath.Join(root, "runs", "*", "attempt-1", "supervisor_verdict.json"), 0)
	assertGlobCount(t, filepath.Join(root, "runs", "*", "attempt-2"), 0)
}

func TestPrepareGLMExecutorPlanFailsFastWithoutToken(t *testing.T) {
	t.Parallel()
	for _, token := range []string{"", "   "} {
		token := token
		t.Run("token="+token, func(t *testing.T) {
			t.Parallel()
			opts := Options{GLMAuthToken: token}
			// The token check runs before any task-derived work, so an empty
			// task is sufficient to exercise the fail-fast path.
			_, _, _, err := prepareGLMExecutorPlan(opts, task.Task{}, t.TempDir(), "prompt", t.TempDir())
			if err == nil {
				t.Fatal("expected fail-fast error when GLM token is missing")
			}
			if !strings.Contains(err.Error(), "glm_api_key") {
				t.Fatalf("error must name the missing config key, got %q", err)
			}
		})
	}
}

func TestExecutorVerificationCmdGLM(t *testing.T) {
	t.Parallel()
	if got := executorVerificationCmd("glm"); got != "claude -p (glm)" {
		t.Fatalf("executorVerificationCmd(glm) = %q, want %q", got, "claude -p (glm)")
	}
}
