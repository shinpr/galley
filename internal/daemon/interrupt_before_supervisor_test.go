package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shinpr/galley/internal/runartifact"
	"github.com/shinpr/galley/internal/task"
)

func TestExecutorProviderTerminalInterruptionsSkipSupervisor(t *testing.T) {
	tests := []struct {
		name         string
		cli          string
		executorBody func(supervisorMarker string) string
		wantDetail   string
	}{
		{
			name: "claude api error result",
			cli:  "claude",
			executorBody: func(_ string) string {
				return `echo '{"type":"result","subtype":"error_during_execution","is_error":true,"session_id":"sess-1","error":"rate limited"}'` + "\n"
			},
			wantDetail: "message=rate limited",
		},
		{
			name: "codex turn.failed",
			cli:  "codex",
			executorBody: func(_ string) string {
				return `printf '%s\n' '{"type":"turn.failed","error":{"code":"quota_exceeded","message":"out of quota"}}'` + "\n"
			},
			wantDetail: "code=quota_exceeded",
		},
		{
			name: "grok non-end-turn",
			cli:  "grok",
			executorBody: func(_ string) string {
				return `printf '%s\n' '{"text":"{}","stopReason":"Cancelled","sessionId":"grok-1"}'` + "\n"
			},
			wantDetail: "stop_reason=Cancelled",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), ".agent-workflow")
			repo := initDaemonGitRepo(t)
			promptPath, schemaPath := writeDaemonPromptFiles(t)
			supervisorMarker := filepath.Join(t.TempDir(), "supervisor.called")
			// A missing supervisor_verdict.json is the robust proof no supervisor
			// ran; the marker is a redundant guard.
			claudeBin := writeFakeClaude(t, tc.executorBody(supervisorMarker))
			opts := Options{Root: root, SystemPromptFile: promptPath, JSONSchemaFile: schemaPath, Once: true, MaxConcurrentTasks: 1, Supervisor: "claude", ClaudeBin: claudeBin}
			switch tc.cli {
			case "codex":
				opts.CodexBin = writeFakeCodexExecutor(t, tc.executorBody(supervisorMarker))
			case "grok":
				opts.GrokBin = writeFakeCommand(t, "grok", "cat >/dev/null 2>&1 || true\n"+tc.executorBody(supervisorMarker))
			}

			taskPath := filepath.Join(root, "tasks", "queued", "task.yaml")
			if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
				t.Fatal(err)
			}
			writeDaemonTask(t, taskPath, repo)
			loaded, err := task.Load(taskPath)
			if err != nil {
				t.Fatal(err)
			}
			loaded.Executor.CLI = tc.cli
			if err := task.Save(taskPath, loaded); err != nil {
				t.Fatal(err)
			}
			// loop_budget > 1 proves the interruption never starts attempt 2.
			setLoopBudget(t, taskPath, 3)

			_ = runTestDaemon(context.Background(), opts)

			failed, err := task.Load(filepath.Join(root, "tasks", "failed", "task.yaml"))
			if err != nil {
				t.Fatalf("failed task missing: %v", err)
			}
			if failed.Status != "failed" {
				t.Fatalf("status = %q; want failed", failed.Status)
			}
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
			if !strings.Contains(attempt.Error.Message, tc.wantDetail) {
				t.Fatalf("interruption message = %q; want provider detail %q", attempt.Error.Message, tc.wantDetail)
			}
			if attempt.Error.ArtifactDir == "" {
				t.Fatal("interruption must record the artifact directory")
			}
			if _, statErr := os.Stat(supervisorMarker); statErr == nil {
				t.Fatal("supervisor was invoked for an interrupted attempt")
			}
			assertGlobCount(t, filepath.Join(root, "runs", "*", "attempt-1", runartifact.ExecutorTerminalFilename), 1)
			assertGlobCount(t, filepath.Join(root, "runs", "*", "attempt-1", "supervisor_verdict.json"), 0)
			assertGlobCount(t, filepath.Join(root, "runs", "*", "attempt-2"), 0)
		})
	}
}

func TestNormalTerminalInvalidResultKeepsSupervisorLoop(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	promptPath, schemaPath := writeDaemonPromptFiles(t)
	validResult := `{"status":"completed","summary":"done","files_modified":["daemon-output.txt"],"acceptance_criteria":[{"id":"AC1","status":"satisfied","evidence":["diff"],"notes":"done"}],"verification":[],"scope_expansions":[],"decisions":[],"risks":[]}`
	// Attempt 1 emits a normal result event wrapping invalid JSON (reviewable
	// parse failure). Attempt 2 emits a valid result and is accepted.
	claudeBin := writeFakeClaude(t, `if [ -f retry.marker ]; then
echo change > daemon-output.txt
echo '`+validResult+`'
else
touch retry.marker
echo '{"type":"result","subtype":"success","is_error":false,"result":"not-json"}'
fi
`)
	taskPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, taskPath, repo)
	setLoopBudget(t, taskPath, 3)

	if err := runTestDaemon(context.Background(), Options{Root: root, SystemPromptFile: promptPath, JSONSchemaFile: schemaPath, Once: true, MaxConcurrentTasks: 1, Supervisor: "claude", ClaudeBin: claudeBin}); err != nil {
		t.Fatal(err)
	}

	done, err := task.Load(filepath.Join(root, "tasks", "done", "task.yaml"))
	if err != nil {
		t.Fatalf("done task missing: %v", err)
	}
	if done.Status != "accepted" {
		t.Fatalf("status = %q; want accepted", done.Status)
	}
	if len(done.Attempts) != 2 {
		t.Fatalf("attempts = %d; want 2: %#v", len(done.Attempts), done.Attempts)
	}
	if done.Attempts[0].SupervisorVerdict != "needs_revision" || done.Attempts[1].SupervisorVerdict != "accepted" {
		t.Fatalf("verdicts = %q, %q; want needs_revision then accepted", done.Attempts[0].SupervisorVerdict, done.Attempts[1].SupervisorVerdict)
	}
	if done.Attempts[0].Error != nil && done.Attempts[0].Error.Kind == task.AttemptKindExecutorInterrupted {
		t.Fatalf("attempt 1 must be reviewed, not interrupted: %#v", done.Attempts[0])
	}
}

func TestInterruptedDirtyWorktreeIsPreservedAndRequeueable(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	promptPath, schemaPath := writeDaemonPromptFiles(t)
	validResult := `{"status":"completed","summary":"resumed","files_modified":["README.md","untracked.txt"],"acceptance_criteria":[{"id":"AC1","status":"satisfied","evidence":["diff"],"notes":"done"}],"verification":[],"scope_expansions":[],"decisions":[],"risks":[]}`
	// First run: modify a tracked file and create an untracked file, then emit an
	// explicit provider API-error terminal (interruption). Second run (reused
	// worktree): assert the retained changes are visible, then complete normally
	// so the supervisor accepts.
	claudeBin := writeFakeClaude(t, `if grep -q RESUMED README.md 2>/dev/null && [ -f untracked.txt ]; then
echo daemon-output > daemon-output.txt
echo '`+validResult+`'
else
echo RESUMED >> README.md
echo created > untracked.txt
echo '{"type":"result","subtype":"error_during_execution","is_error":true,"error":"transient provider failure before completion"}'
fi
`)
	taskPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, taskPath, repo)
	setLoopBudget(t, taskPath, 3)

	opts := Options{Root: root, SystemPromptFile: promptPath, JSONSchemaFile: schemaPath, Once: true, MaxConcurrentTasks: 1, Supervisor: "claude", ClaudeBin: claudeBin}
	_ = runTestDaemon(context.Background(), opts)

	failedPath := filepath.Join(root, "tasks", "failed", "task.yaml")
	failed, err := task.Load(failedPath)
	if err != nil {
		t.Fatalf("failed task missing: %v", err)
	}
	if failed.Status != "failed" || len(failed.Attempts) != 1 || failed.Attempts[0].Error == nil || failed.Attempts[0].Error.Kind != task.AttemptKindExecutorInterrupted {
		t.Fatalf("expected a single interrupted attempt: %#v", failed.Attempts)
	}

	diff := string(mustReadSingleGlob(t, filepath.Join(root, "runs", "*", "attempt-1", runartifact.DiffPatchFilename)))
	if !strings.Contains(diff, "RESUMED") {
		t.Fatalf("diff.patch missing tracked modification: %s", diff)
	}
	if !strings.Contains(diff, "untracked.txt") || !strings.Contains(diff, "created") {
		t.Fatalf("diff.patch missing untracked file: %s", diff)
	}

	worktreePath := failed.Worktree.Path
	if !filepath.IsAbs(worktreePath) {
		worktreePath = filepath.Join(repo, worktreePath)
	}
	readme, err := os.ReadFile(filepath.Join(worktreePath, "README.md"))
	if err != nil || !strings.Contains(string(readme), "RESUMED") {
		t.Fatalf("retained worktree lost tracked change: %q err=%v", readme, err)
	}
	if _, err := os.Stat(filepath.Join(worktreePath, "untracked.txt")); err != nil {
		t.Fatalf("retained worktree lost untracked change: %v", err)
	}

	if _, err := task.Requeue(failedPath, task.RequeueOptions{Root: root, Reason: "interruption cause resolved"}); err != nil {
		t.Fatalf("requeue: %v", err)
	}
	if err := runTestDaemon(context.Background(), opts); err != nil {
		t.Fatalf("second run: %v", err)
	}
	done, err := task.Load(filepath.Join(root, "tasks", "done", "task.yaml"))
	if err != nil {
		t.Fatalf("done task missing after requeue: %v", err)
	}
	if done.Status != "accepted" {
		t.Fatalf("status after requeue = %q; want accepted", done.Status)
	}
	var reused bool
	for _, m := range mustGlob(t, filepath.Join(root, "runs", "*", runartifact.WorkspaceFilename)) {
		if strings.Contains(string(mustReadFile(t, m)), `"worktree_reused": true`) {
			reused = true
		}
	}
	if !reused {
		t.Fatal("second run did not reuse the retained worktree")
	}
}

func TestInterruptedAttemptSurvivesPostExecutorEvidenceFailures(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	promptPath, schemaPath := writeDaemonPromptFiles(t)
	// The executor changes the workspace, then emits a provider API-error
	// terminal (interruption).
	claudeBin := writeFakeClaude(t, "echo change > daemon-output.txt\necho '{\"type\":\"result\",\"subtype\":\"error_during_execution\",\"is_error\":true,\"session_id\":\"sess-x\",\"error\":\"provider outage\"}'\n")

	// Inject a review-staging failure and corrupt the diff target so both the
	// staging and diff writes fail; neither may override the interruption route.
	stageFail := func(_ context.Context, _ Options, _, attemptDir string, _ []string) error {
		if err := os.MkdirAll(filepath.Join(attemptDir, runartifact.GitStatusFilename), 0o700); err != nil {
			return err
		}
		return fmt.Errorf("git add -A (review staging) failed: simulated index lock")
	}

	taskPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, taskPath, repo)
	// loop_budget > 1 proves the interruption never starts attempt 2.
	setLoopBudget(t, taskPath, 3)

	_ = runTestDaemon(context.Background(), Options{
		Root: root, SystemPromptFile: promptPath, JSONSchemaFile: schemaPath,
		Once: true, MaxConcurrentTasks: 1, Supervisor: "claude", ClaudeBin: claudeBin,
		dependencies: &daemonDependencies{stageExecutorOutput: stageFail},
	})

	failed, err := task.Load(filepath.Join(root, "tasks", "failed", "task.yaml"))
	if err != nil {
		t.Fatalf("failed task missing: %v", err)
	}
	if failed.Status != "failed" {
		t.Fatalf("status = %q; want failed", failed.Status)
	}
	if len(failed.Attempts) != 1 {
		t.Fatalf("attempts = %d; want exactly one executor attempt: %#v", len(failed.Attempts), failed.Attempts)
	}
	attempt := failed.Attempts[0]
	if attempt.SupervisorVerdict != task.AttemptVerdictNotReviewed {
		t.Fatalf("verdict = %q; want %q", attempt.SupervisorVerdict, task.AttemptVerdictNotReviewed)
	}
	if attempt.Error == nil || attempt.Error.Kind != task.AttemptKindExecutorInterrupted {
		t.Fatalf("attempt error = %#v; want kind %q (a post-executor evidence failure must not reclassify the interruption)", attempt.Error, task.AttemptKindExecutorInterrupted)
	}
	if !strings.Contains(attempt.Error.Message, "provider outage") {
		t.Fatalf("interruption message must retain provider detail: %q", attempt.Error.Message)
	}
	assertGlobCount(t, filepath.Join(root, "runs", "*", "attempt-1", runartifact.RunResultFilename), 1)
	assertGlobCount(t, filepath.Join(root, "runs", "*", "attempt-1", runartifact.ExecutorTerminalFilename), 1)
	assertGlobCount(t, filepath.Join(root, "runs", "*", "attempt-1", "supervisor_verdict.json"), 0)
	assertGlobCount(t, filepath.Join(root, "runs", "*", "attempt-2"), 0)
}

func mustGlob(t *testing.T, pattern string) []string {
	t.Helper()
	matches, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatalf("glob %s: %v", pattern, err)
	}
	return matches
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}
