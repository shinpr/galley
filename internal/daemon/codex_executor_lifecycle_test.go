package daemon

// AC: AC4 — Run evidence, task show/list behavior, retry handling, idle
// timeout handling, and supervisor handoff remain coherent when the selected
// executor is Codex.
//
// Behavior under test:
//   - Trigger: daemon Run processes a Codex (executor.cli="codex") task
//     across three lifecycle scenarios: success, executor failure with
//     retry, and idle timeout.
//   - Process: runExecutorAttempt persists per-attempt evidence under
//     runs/<id>/attempt-N/, the supervisor (fake claude/codex supervisor in
//     daemon tests) consumes the executor result, and the daemon retry
//     loop schedules additional attempts within loop_budget when the
//     verdict is needs_revision/hard_stop semantics already used for Claude.
//   - Observable result for Codex parity:
//       * On success: tasks/done/<id>.yaml status="accepted",
//         attempts[0].executor_status (claude_status field today) reports
//         "completed", and runs/<id>/attempt-1/ contains command_plan.json,
//         run_result.json, an executor result JSON (executor_result.json), a
//         supervisor_verdict.json, git_status.json, and diff.patch — i.e.
//         parity with the Claude success artifact set.
//       * On retry: a Codex failure on attempt-1 produces attempt-2 with
//         its own attempt-N directory and the loop_budget is respected.
//       * On idle timeout: a Codex executor that produces no stdout
//         progress within Options.IdleTimeout is terminated, run_result
//         carries the idle-timeout classification, and the task ends in
//         tasks/failed with a coherent attempt summary.
//
// @lane: integration
// @category: persistence
// @dependency: daemon Run, runner Codex adapter, fake codex executor binary,
//   fake supervisor (writeFakeClaude supervisor block / writeFakeCodexSupervisor)
// @complexity: high
// @roi: business_value=10 * user_frequency=8 + legal=0 + defect=10 -> 90
// @timing: alongside implementation (after AC2 dispatch lands)
// @placement: separate file from codex_executor_dispatch_test.go so retry/
// timeout lifecycle review can be inspected independently from raw dispatch
// wiring (see review_dimensions.reviewable-diff-shape and evidence-routing).

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shinpr/galley/internal/runartifact"
	"github.com/shinpr/galley/internal/task"
)

// codexAcceptedExecutorResult is the executor result JSON line that fakes the
// Codex executor reporting a clean "completed" attempt to the supervisor.
const codexAcceptedExecutorResult = `{"status":"completed","summary":"codex done","files_modified":["daemon-output.txt"],"acceptance_criteria":[{"id":"AC1","status":"satisfied","evidence":["diff"],"notes":"done"}],"verification":[],"scope_expansions":[],"decisions":[],"risks":[]}`

// codexRiskyExecutorResult mirrors the Claude completed_with_risks path that
// writeFakeClaude's supervisor block maps to needs_revision, exercising the
// in-loop retry semantics for the Codex executor.
const codexRiskyExecutorResult = `{"status":"completed_with_risks","summary":"codex risky","files_modified":["retry.marker"],"acceptance_criteria":[{"id":"AC1","status":"satisfied","evidence":["diff"],"notes":"done"}],"verification":[],"scope_expansions":[],"decisions":[],"risks":[{"type":"partial_verification","detail":"needs retry","mitigation":"retry with corrective work order","needs_human_review":true}]}`

// TestCodexExecutorSuccessProducesParityRunEvidence asserts the AC4 happy
// path: a Codex task that succeeds writes the same per-attempt evidence
// surface as the Claude path.
func TestCodexExecutorSuccessProducesParityRunEvidence(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	promptPath, schemaPath := writeDaemonPromptFiles(t)

	claudeBin := writeFakeClaude(t, "exit 1\n") // never used as executor for cli=codex.
	codexBin := writeFakeCodexExecutor(t, "echo change > daemon-output.txt\necho '"+codexAcceptedExecutorResult+"'\n")

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
	setLoopBudget(t, taskPath, 2)

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

	doneTask, err := task.Load(filepath.Join(root, "tasks", "done", "task.yaml"))
	if err != nil {
		t.Fatalf("done task missing: %v", err)
	}
	if doneTask.Status != "accepted" {
		t.Fatalf("status got %q", doneTask.Status)
	}
	if len(doneTask.Attempts) != 1 || doneTask.Attempts[0].ClaudeStatus != "completed" {
		t.Fatalf("attempts got %#v", doneTask.Attempts)
	}
	if !strings.Contains(doneTask.Attempts[0].Summary, "workspace=") {
		t.Fatalf("attempt summary missing workspace marker: %q", doneTask.Attempts[0].Summary)
	}

	// Parity with the Claude success evidence surface from
	// TestRunOnceMovesTaskToDoneAndWritesRunEvidence: every artifact that
	// matters for task show/list, supervisor handoff, and PR rendering must
	// exist exactly once under attempt-1/.
	assertGlobCount(t, filepath.Join(root, "runs", "*", "attempt-1", "command_plan.json"), 1)
	assertGlobCount(t, filepath.Join(root, "runs", "*", "attempt-1", "run_result.json"), 1)
	assertGlobCount(t, filepath.Join(root, "runs", "*", "attempt-1", runartifact.ExecutorResultFilename), 1)
	assertGlobCount(t, filepath.Join(root, "runs", "*", "attempt-1", "supervisor_verdict.json"), 1)
	assertGlobCount(t, filepath.Join(root, "runs", "*", "attempt-1", "git_status.json"), 1)
	assertGlobCount(t, filepath.Join(root, "runs", "*", "attempt-1", "diff.patch"), 1)
	// The Codex adapter mirrors the claude.stdout.jsonl convention with
	// codex.stdout.jsonl so task show/list and supervisor handoff stay
	// coherent across providers.
	assertGlobCount(t, filepath.Join(root, "runs", "*", "attempt-1", "codex.stdout.jsonl"), 1)

	// Run evidence must record the executor command that actually ran. A
	// Codex attempt that still reports `claude -p` would mislead reviewers
	// inspecting tasks/done/<id>.yaml about which executor produced the
	// evidence, so the executor verification entry has to reflect the
	// selected CLI.
	var sawCodexVerificationCmd, sawClaudePExec bool
	for _, vc := range doneTask.Verification.Commands {
		switch vc.Cmd {
		case "codex exec":
			sawCodexVerificationCmd = true
		case "claude -p":
			sawClaudePExec = true
		}
	}
	if !sawCodexVerificationCmd {
		t.Fatalf("expected verification.commands to record `codex exec` for codex executor attempt: %#v", doneTask.Verification.Commands)
	}
	if sawClaudePExec {
		t.Fatalf("verification.commands must not record `claude -p` for codex executor attempt: %#v", doneTask.Verification.Commands)
	}
}

// TestCodexExecutorRetryRespectsLoopBudget asserts the AC4 retry path: a
// Codex failure followed by an accepting supervisor on the retry produces a
// second attempt directory and respects loop_budget.
func TestCodexExecutorRetryRespectsLoopBudget(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	promptPath, schemaPath := writeDaemonPromptFiles(t)

	claudeBin := writeFakeClaude(t, "exit 1\n")
	codexBin := writeFakeCodexExecutor(t, `if [ -f retry.marker ]; then
echo change > daemon-output.txt
echo '`+codexAcceptedExecutorResult+`'
else
touch retry.marker
echo '`+codexRiskyExecutorResult+`'
fi
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
	setLoopBudget(t, taskPath, 3)

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

	doneTask, err := task.Load(filepath.Join(root, "tasks", "done", "task.yaml"))
	if err != nil {
		t.Fatalf("done task missing: %v", err)
	}
	if doneTask.Status != "accepted" {
		t.Fatalf("status got %q", doneTask.Status)
	}
	if len(doneTask.Attempts) != 2 {
		t.Fatalf("attempts got %d, want 2: %#v", len(doneTask.Attempts), doneTask.Attempts)
	}
	if doneTask.Attempts[0].SupervisorVerdict != "needs_revision" || doneTask.Attempts[1].SupervisorVerdict != "accepted" {
		t.Fatalf("attempt verdicts got %#v", doneTask.Attempts)
	}

	assertGlobCount(t, filepath.Join(root, "runs", "*", "attempt-1", "command_plan.json"), 1)
	assertGlobCount(t, filepath.Join(root, "runs", "*", "attempt-1", "run_result.json"), 1)
	assertGlobCount(t, filepath.Join(root, "runs", "*", "attempt-2", "command_plan.json"), 1)
	assertGlobCount(t, filepath.Join(root, "runs", "*", "attempt-2", "run_result.json"), 1)
	// loop_budget=3 was set but only two attempts were necessary; the
	// supervisor accepted on attempt-2 so the loop must not spin up a third
	// attempt directory.
	assertGlobCount(t, filepath.Join(root, "runs", "*", "attempt-3"), 0)
}

// TestCodexExecutorIdleTimeoutClassifiedConsistently asserts the AC4 idle
// timeout path: when the Codex executor stalls without progress, the daemon
// kills it, records the run_result idle-timeout classification, and the
// task ends in tasks/failed with a coherent attempt summary.
func TestCodexExecutorIdleTimeoutClassifiedConsistently(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	promptPath, schemaPath := writeDaemonPromptFiles(t)

	claudeBin := writeFakeClaude(t, "exit 1\n")
	// `cat >/dev/null` is provided by writeFakeCodexExecutor; the body below
	// just stalls without writing to stdout, so the runner's idle-output
	// watchdog has to terminate the process.
	codexBin := writeFakeCodexExecutor(t, "sleep 5\n")

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
	// loop_budget=1 keeps the test scoped to the idle-timeout classification
	// surface; retry semantics are covered by
	// TestCodexExecutorRetryRespectsLoopBudget.
	setLoopBudget(t, taskPath, 1)

	_ = runTestDaemon(context.Background(), Options{
		Root:               root,
		SystemPromptFile:   promptPath,
		JSONSchemaFile:     schemaPath,
		Once:               true,
		MaxConcurrentTasks: 1,
		Supervisor:         "claude",
		ClaudeBin:          claudeBin,
		CodexBin:           codexBin,
		IdleTimeout:        200 * time.Millisecond,
	})

	failedTask, err := task.Load(filepath.Join(root, "tasks", "failed", "task.yaml"))
	if err != nil {
		t.Fatalf("failed task missing: %v", err)
	}
	if len(failedTask.Attempts) == 0 {
		t.Fatal("expected at least one attempt")
	}
	// The executor's idle-timeout signal must surface through the same
	// fields the Claude path uses so `galley task show` and supervisor handoff
	// stay coherent across providers.
	var sawIdle bool
	for _, a := range failedTask.Attempts {
		if a.ClaudeStatus == "idle_timed_out" {
			sawIdle = true
		}
		if a.Error != nil && a.Error.Kind == "idle_timeout" {
			sawIdle = true
		}
	}
	if !sawIdle {
		t.Fatalf("no attempt carries idle_timeout classification: %#v", failedTask.Attempts)
	}

	// run_result.json must record the idle-timeout flag the runner emits, so
	// downstream tooling (task show/list, supervisor evidence) can detect it
	// without re-parsing stderr.
	runResultData := mustReadSingleGlob(t, filepath.Join(root, "runs", "*", "attempt-1", "run_result.json"))
	var runResult struct {
		IdleTimedOut bool `json:"idle_timed_out"`
	}
	if err := json.Unmarshal(runResultData, &runResult); err != nil {
		t.Fatalf("decode run_result.json: %v", err)
	}
	if !runResult.IdleTimedOut {
		t.Fatalf("run_result.json idle_timed_out not set: %s", runResultData)
	}
}
