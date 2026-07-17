package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shinpr/galley/internal/runartifact"
	"github.com/shinpr/galley/internal/runner"
	"github.com/shinpr/galley/internal/task"
)

func TestAmbiguousClaudeResultFailsClosed(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	promptPath, schemaPath := writeDaemonPromptFiles(t)
	supervisorMarker := filepath.Join(t.TempDir(), "supervisor.called")
	// A raw fake (not writeFakeClaude) so no success terminal is auto-appended:
	// the executor emits only an ambiguous result. The supervisor branch records
	// any invocation, which an interruption must never trigger.
	claudeBin := writeFakeCommand(t, "claude", `for arg in "$@"; do
  if [ "$arg" = "--no-session-persistence" ]; then
    echo called >> `+supervisorMarker+`
    cat >/dev/null
    printf '%s\n' '{"status":"accepted","summary":"x","acceptance_gaps":[],"reviewed_files":[],"acceptance_evidence":[],"findings":[],"quality_coverage":[],"residual_risks":[],"discussion_items":[],"confidence":"high","next_work_order":""}'
    exit 0
  fi
done
echo change > daemon-output.txt
printf '%s\n' '{"type":"result","subtype":"in_progress","session_id":"amb-1"}'
`)
	taskPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, taskPath, repo)
	setLoopBudget(t, taskPath, 3)

	_ = runTestDaemon(context.Background(), Options{Root: root, SystemPromptFile: promptPath, JSONSchemaFile: schemaPath, Once: true, MaxConcurrentTasks: 1, Supervisor: "claude", ClaudeBin: claudeBin})

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
	if !strings.Contains(attempt.Error.Message, runner.TerminalReasonAmbiguousResult) {
		t.Fatalf("message must name the ambiguous reason: %q", attempt.Error.Message)
	}
	if !strings.Contains(attempt.Error.Message, "status=in_progress") || !strings.Contains(attempt.Error.Message, "session_id=amb-1") {
		t.Fatalf("message must retain ambiguous result detail: %q", attempt.Error.Message)
	}
	if _, err := os.Stat(supervisorMarker); err == nil {
		t.Fatal("supervisor was invoked for an ambiguous (interrupted) attempt")
	}
	assertNormalTerminal(t, root, false)
	assertGlobCount(t, filepath.Join(root, "runs", "*", "attempt-1", "supervisor_verdict.json"), 0)
	assertGlobCount(t, filepath.Join(root, "runs", "*", "attempt-2"), 0)
}

func TestGrokEndTurnWithoutSessionIDStaysReviewable(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	promptPath, schemaPath := writeDaemonPromptFiles(t)
	claudeBin := writeFakeClaude(t, "")
	grokBin := writeFakeCommand(t, "grok", "cat >/dev/null 2>&1 || true\nprintf '%s\\n' '{\"text\":\"{}\",\"stopReason\":\"EndTurn\"}'\n")

	taskPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, taskPath, repo)
	loaded, err := task.Load(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Executor.CLI = "grok"
	if err := task.Save(taskPath, loaded); err != nil {
		t.Fatal(err)
	}

	_ = runTestDaemon(context.Background(), Options{Root: root, SystemPromptFile: promptPath, JSONSchemaFile: schemaPath, Once: true, MaxConcurrentTasks: 1, Supervisor: "claude", ClaudeBin: claudeBin, GrokBin: grokBin})

	// A normal terminal plus a persisted verdict means a real supervisor review,
	// not an interruption. needs_supervisor_review shares the failed directory,
	// so the status/verdict is the routing signal, not the directory.
	assertNormalTerminal(t, root, true)
	assertGlobCount(t, filepath.Join(root, "runs", "*", "attempt-1", "supervisor_verdict.json"), 1)
	reviewed, err := task.Load(filepath.Join(root, "tasks", "failed", "task.yaml"))
	if err != nil {
		t.Fatalf("reviewed task missing: %v", err)
	}
	if reviewed.Status != task.StatusNeedsSupervisorReview {
		t.Fatalf("status = %q; want %q (a reviewed attempt, not an interruption)", reviewed.Status, task.StatusNeedsSupervisorReview)
	}
	if len(reviewed.Attempts) != 1 {
		t.Fatalf("attempts = %d; want 1: %#v", len(reviewed.Attempts), reviewed.Attempts)
	}
	att := reviewed.Attempts[0]
	if att.SupervisorVerdict != "needs_revision" {
		t.Fatalf("verdict = %q; want a real supervisor verdict (needs_revision)", att.SupervisorVerdict)
	}
	if att.Error != nil && att.Error.Kind == task.AttemptKindExecutorInterrupted {
		t.Fatalf("EndTurn without sessionId must not be classified as an interruption: %#v", att.Error)
	}
}

func TestInterruptionRetainsProviderDetailWhenRunnerFails(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	promptPath, schemaPath := writeDaemonPromptFiles(t)
	// The executor prints a structured provider API error and then exits non-zero
	// (the appended success terminal never runs). The runner failure sets the
	// routing reason; the provider detail must still be retained.
	claudeBin := writeFakeClaude(t, "echo change > daemon-output.txt\necho '{\"type\":\"result\",\"subtype\":\"error_during_execution\",\"is_error\":true,\"session_id\":\"sess-z\",\"error\":\"provider 529\"}'\nexit 7\n")

	taskPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, taskPath, repo)
	setLoopBudget(t, taskPath, 3)

	_ = runTestDaemon(context.Background(), Options{Root: root, SystemPromptFile: promptPath, JSONSchemaFile: schemaPath, Once: true, MaxConcurrentTasks: 1, Supervisor: "claude", ClaudeBin: claudeBin})

	failed := mustLoadFailedTask(t, root)
	if len(failed.Attempts) != 1 {
		t.Fatalf("attempts = %d; want 1: %#v", len(failed.Attempts), failed.Attempts)
	}
	attempt := failed.Attempts[0]
	if attempt.Error == nil || attempt.Error.Kind != task.AttemptKindExecutorInterrupted {
		t.Fatalf("attempt error = %#v; want kind %q", attempt.Error, task.AttemptKindExecutorInterrupted)
	}
	if !strings.Contains(attempt.Error.Message, runner.TerminalReasonExitNonZero) {
		t.Fatalf("runner failure reason must control routing: %q", attempt.Error.Message)
	}
	if !strings.Contains(attempt.Error.Message, "message=provider 529") {
		t.Fatalf("provider detail must be retained even on a runner failure: %q", attempt.Error.Message)
	}
	assertGlobCount(t, filepath.Join(root, "runs", "*", "attempt-1", "supervisor_verdict.json"), 0)
	assertGlobCount(t, filepath.Join(root, "runs", "*", "attempt-2"), 0)
}

func TestInterruptionPartialDiffWriteFailureRetainsComponents(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	promptPath, schemaPath := writeDaemonPromptFiles(t)
	// Stage normally, then pre-create git_status.json as a directory so only that
	// write fails while diff.patch still succeeds.
	stageThenBlockGitStatus := func(ctx context.Context, opts Options, workDir, attemptDir string, exclude []string) error {
		if err := defaultStageExecutorOutput(ctx, opts, workDir, attemptDir, exclude); err != nil {
			return err
		}
		return os.MkdirAll(filepath.Join(attemptDir, runartifact.GitStatusFilename), 0o700)
	}
	claudeBin := writeFakeClaude(t, "echo change > daemon-output.txt\necho '{\"type\":\"result\",\"subtype\":\"error_during_execution\",\"is_error\":true,\"error\":\"provider outage\"}'\n")

	taskPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, taskPath, repo)
	setLoopBudget(t, taskPath, 3)

	_ = runTestDaemon(context.Background(), Options{
		Root: root, SystemPromptFile: promptPath, JSONSchemaFile: schemaPath,
		Once: true, MaxConcurrentTasks: 1, Supervisor: "claude", ClaudeBin: claudeBin,
		dependencies: &daemonDependencies{stageExecutorOutput: stageThenBlockGitStatus},
	})

	failed := mustLoadFailedTask(t, root)
	if len(failed.Attempts) != 1 {
		t.Fatalf("attempts = %d; want 1: %#v", len(failed.Attempts), failed.Attempts)
	}
	if failed.Attempts[0].Error == nil || failed.Attempts[0].Error.Kind != task.AttemptKindExecutorInterrupted {
		t.Fatalf("attempt error = %#v; want kind %q", failed.Attempts[0].Error, task.AttemptKindExecutorInterrupted)
	}

	diff := string(mustReadSingleGlob(t, filepath.Join(root, "runs", "*", "attempt-1", runartifact.DiffPatchFilename)))
	if !strings.Contains(diff, "change") {
		t.Fatalf("diff.patch must retain the captured change: %q", diff)
	}

	foundCaptureRisk := false
	for _, r := range failed.Risks {
		if strings.HasPrefix(r.ID, "interrupted-diff-capture") && strings.Contains(r.Detail, "write git_status.json") {
			foundCaptureRisk = true
		}
	}
	if !foundCaptureRisk {
		t.Fatalf("expected an interrupted-diff-capture risk disclosing the write failure: %#v", failed.Risks)
	}

	foundTruthful := false
	for _, cmd := range failed.Verification.Commands {
		if cmd.Status != "failed" || !strings.Contains(cmd.OutputExcerpt, "executor interrupted") {
			continue
		}
		foundTruthful = true
		preservedText, notCaptured := splitInterruptionExcerpt(t, cmd.OutputExcerpt)
		if !strings.Contains(preservedText, runartifact.DiffPatchFilename) {
			t.Fatalf("preserved inventory must list diff.patch: %q", cmd.OutputExcerpt)
		}
		if strings.Contains(preservedText, runartifact.GitStatusFilename) {
			t.Fatalf("preserved inventory falsely claims git_status.json: %q", cmd.OutputExcerpt)
		}
		if !strings.Contains(notCaptured, runartifact.GitStatusFilename) {
			t.Fatalf("unavailable inventory must list git_status.json: %q", cmd.OutputExcerpt)
		}
	}
	if !foundTruthful {
		t.Fatalf("expected truthful capture-failure verification text: %#v", failed.Verification.Commands)
	}
	assertGlobCount(t, filepath.Join(root, "runs", "*", "attempt-1", "supervisor_verdict.json"), 0)
	assertGlobCount(t, filepath.Join(root, "runs", "*", "attempt-2"), 0)
}

func TestInterruptionArtifactStatusReportsPerArtifactTruth(t *testing.T) {
	t.Run("partial git-status write failure", func(t *testing.T) {
		preserved, failed := interruptionArtifactStatus(attemptOutcome{
			CLI:          "claude",
			GitStatusErr: errors.New("write git_status.json: is a directory"),
		})
		if !containsString(preserved, "diff.patch") {
			t.Fatalf("diff.patch must be preserved: %v", preserved)
		}
		if containsString(preserved, "git_status.json") {
			t.Fatalf("git_status.json must not be preserved: %v", preserved)
		}
		if !containsSubstring(failed, "git_status.json") {
			t.Fatalf("git_status.json must be reported unavailable: %v", failed)
		}
	})
	t.Run("raw output capture failure", func(t *testing.T) {
		preserved, failed := interruptionArtifactStatus(attemptOutcome{
			CLI:          "claude",
			RawOutputErr: errors.New("capture file not created: claude.stdout.jsonl"),
		})
		if containsString(preserved, "raw provider output") {
			t.Fatalf("raw provider output must not be claimed preserved: %v", preserved)
		}
		if !containsSubstring(failed, "raw provider output") {
			t.Fatalf("raw provider output must be reported unavailable: %v", failed)
		}
	})
}

func containsString(items []string, want string) bool {
	for _, s := range items {
		if s == want {
			return true
		}
	}
	return false
}

func containsSubstring(items []string, want string) bool {
	for _, s := range items {
		if strings.Contains(s, want) {
			return true
		}
	}
	return false
}

func splitInterruptionExcerpt(t *testing.T, excerpt string) (preserved, notCaptured string) {
	t.Helper()
	const marker = "preserved under "
	idx := strings.Index(excerpt, marker)
	if idx < 0 {
		t.Fatalf("excerpt missing preserved inventory: %q", excerpt)
	}
	rest := excerpt[idx+len(marker):]
	if colon := strings.Index(rest, ": "); colon >= 0 {
		rest = rest[colon+2:]
	}
	const cut = "; not captured: "
	if at := strings.Index(rest, cut); at >= 0 {
		return rest[:at], rest[at+len(cut):]
	}
	return rest, ""
}

func mustLoadFailedTask(t *testing.T, root string) task.Task {
	t.Helper()
	failed, err := task.Load(filepath.Join(root, "tasks", "failed", "task.yaml"))
	if err != nil {
		t.Fatalf("failed task missing: %v", err)
	}
	if failed.Status != "failed" {
		t.Fatalf("status = %q; want failed", failed.Status)
	}
	return failed
}

func assertNormalTerminal(t *testing.T, root string, want bool) {
	t.Helper()
	data := mustReadSingleGlob(t, filepath.Join(root, "runs", "*", "attempt-1", runartifact.ExecutorTerminalFilename))
	var terminal runner.ExecutorTerminal
	if err := json.Unmarshal(data, &terminal); err != nil {
		t.Fatalf("decode executor_terminal.json: %v", err)
	}
	if terminal.NormalTerminal != want {
		t.Fatalf("NormalTerminal = %t; want %t (reason=%q)", terminal.NormalTerminal, want, terminal.Reason)
	}
}
