package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shinpr/galley/internal/runartifact"
	"github.com/shinpr/galley/internal/task"
)

// codexAcceptedExecutorResult is the executor result JSON line that fakes the
// Codex executor reporting a clean "completed" attempt to the supervisor.
const codexAcceptedExecutorResult = `{"status":"completed","summary":"codex done","files_modified":["daemon-output.txt"],"acceptance_criteria":[{"id":"AC1","status":"satisfied","evidence":["diff"],"notes":"done"}],"verification":[],"scope_expansions":[],"decisions":[],"risks":[]}`

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
