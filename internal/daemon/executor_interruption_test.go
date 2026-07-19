package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/shinpr/galley/internal/executorflow"
	"github.com/shinpr/galley/internal/runartifact"
	"github.com/shinpr/galley/internal/runner"
	"github.com/shinpr/galley/internal/supervisor"
	"github.com/shinpr/galley/internal/task"
	"github.com/shinpr/galley/internal/workspace"
)

func countingSupervisor(calls *int32, verdict func(attempt int) supervisor.Verdict) func(context.Context, Options, supervisor.Evidence, string, string) (supervisor.Verdict, error) {
	return func(_ context.Context, _ Options, _ supervisor.Evidence, _, _ string) (supervisor.Verdict, error) {
		n := atomic.AddInt32(calls, 1)
		return verdict(int(n)), nil
	}
}

func withSupervisorSeam(opts Options, seam func(context.Context, Options, supervisor.Evidence, string, string) (supervisor.Verdict, error)) Options {
	deps := opts.daemonDependencies()
	deps.supervisorRunner = seam
	opts.dependencies = &deps
	return opts
}

// writeRawExecutor writes a fake executor binary that emits exactly body on
// stdout without the shared happy-path helpers' appended normal-terminal event,
// so interruption cases control the provider terminal the daemon classifies.
func writeRawExecutor(t *testing.T, name, body string) string {
	t.Helper()
	return writeFakeCommand(t, name, "cat >/dev/null 2>/dev/null || true\n"+body)
}

func TestInterruptedExecutorBypassesSupervisor(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{name: "empty worktree", body: "exit 1\n"},
		{name: "dirty worktree", body: "echo partial > daemon-output.txt\necho scratch > untracked.txt\nexit 1\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), ".agent-workflow")
			repo := initDaemonGitRepo(t)
			promptPath, schemaPath := writeDaemonPromptFiles(t)
			claudeBin := writeFakeClaude(t, tc.body)
			taskPath := filepath.Join(root, "tasks", "queued", "task.yaml")
			if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
				t.Fatal(err)
			}
			writeDaemonTask(t, taskPath, repo)
			setLoopBudget(t, taskPath, 3)

			var supCalls int32
			opts := testDaemonOptions(Options{Root: root, SystemPromptFile: promptPath, JSONSchemaFile: schemaPath, Once: true, MaxConcurrentTasks: 1, Supervisor: "claude", ClaudeBin: claudeBin})
			opts = withSupervisorSeam(opts, countingSupervisor(&supCalls, func(int) supervisor.Verdict {
				t.Errorf("supervisor must not run for an interrupted executor")
				return supervisor.Verdict{Status: "accepted"}
			}))
			_ = Run(context.Background(), opts)

			assertInterruptedFailedTask(t, root, &supCalls)
		})
	}
}

// Representative provider wiring across the Claude, Codex, and Grok transports;
// the exhaustive classification matrix lives in internal/runner/terminal_test.go.
// Each fixture exits 0, so an interruption must come from the provider terminal,
// not the exit code.
func TestExecutorInterruptionLifecycleMatrix(t *testing.T) {
	cases := []struct {
		name         string
		cli          string
		stdout       string
		wantReason   string
		wantProvider string
		wantMessage  []string
		notMessage   []string
	}{
		{
			name:         "claude api failure",
			cli:          "claude",
			stdout:       `printf '%s\n' '{"type":"result","subtype":"success","is_error":true,"api_error_status":529,"terminal_reason":"api_error","stop_reason":"stop_sequence","session_id":"claude-sess","result":"api overloaded"}'` + "\n",
			wantReason:   "claude_result_error",
			wantProvider: "claude",
			wantMessage:  []string{"claude_result_error", "api_error", "529", "stop_sequence", "claude-sess", "api overloaded"},
		},
		{
			name:         "codex turn.failed with detail",
			cli:          "codex",
			stdout:       `printf '%s\n' '{"type":"turn.failed","error":{"message":"model overloaded","code":"rate_limit"}}'` + "\n",
			wantReason:   "codex_turn_failed",
			wantProvider: "codex",
			wantMessage:  []string{"codex_turn_failed", "rate_limit", "model overloaded"},
		},
		{
			name:         "grok non-endturn",
			cli:          "grok",
			stdout:       `printf '%s\n' '{"text":"partial","stopReason":"MaxTokens","sessionId":"grok-sess"}'` + "\n",
			wantReason:   "grok_non_end_turn",
			wantProvider: "grok",
			wantMessage:  []string{"grok_non_end_turn", "MaxTokens"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), ".agent-workflow")
			repo := initDaemonGitRepo(t)
			promptPath, schemaPath := writeDaemonPromptFiles(t)
			taskPath := filepath.Join(root, "tasks", "queued", "task.yaml")
			if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
				t.Fatal(err)
			}
			writeDaemonTask(t, taskPath, repo)
			setExecutorCLI(t, taskPath, tc.cli)
			setLoopBudget(t, taskPath, 2)

			opts := Options{Root: root, SystemPromptFile: promptPath, JSONSchemaFile: schemaPath, Once: true, MaxConcurrentTasks: 1, Supervisor: "claude"}
			switch tc.cli {
			case "codex":
				opts.ClaudeBin = writeFakeClaude(t, "exit 1\n")
				opts.CodexBin = writeRawExecutor(t, "codex", tc.stdout)
			case "grok":
				opts.ClaudeBin = writeFakeClaude(t, "exit 1\n")
				opts.GrokBin = writeRawExecutor(t, "grok", tc.stdout)
			case "glm":
				opts.ClaudeBin = writeRawExecutor(t, "claude", tc.stdout)
				opts.GLMAuthToken = "test-glm-token"
			default:
				opts.ClaudeBin = writeRawExecutor(t, "claude", tc.stdout)
			}

			var supCalls int32
			opts = testDaemonOptions(opts)
			opts = withSupervisorSeam(opts, countingSupervisor(&supCalls, func(int) supervisor.Verdict {
				t.Errorf("supervisor must not run for a %s interruption", tc.name)
				return supervisor.Verdict{Status: "accepted"}
			}))
			_ = Run(context.Background(), opts)

			failed := assertInterruptedFailedTask(t, root, &supCalls)
			last := failed.Attempts[len(failed.Attempts)-1]
			if last.Error.Kind != "executor_interrupted" {
				t.Fatalf("error kind = %q, want executor_interrupted", last.Error.Kind)
			}
			for _, want := range tc.wantMessage {
				if !strings.Contains(last.Error.Message, want) {
					t.Fatalf("attempt error message missing %q: %q", want, last.Error.Message)
				}
			}
			for _, unwanted := range tc.notMessage {
				if strings.Contains(last.Error.Message, unwanted) {
					t.Fatalf("attempt error message must not contain %q: %q", unwanted, last.Error.Message)
				}
			}
			// task show consumes these fields to render the interruption and
			// requeue recovery (see cli.isExecutorInterruption).
			if last.Error.Phase != "executor" || last.SupervisorVerdict != "not_reviewed" || last.Error.ArtifactDir == "" {
				t.Fatalf("task-show fields incomplete: %#v", last.Error)
			}
			terminalData := mustReadSingleGlob(t, filepath.Join(root, "runs", "*", "attempt-1", runartifact.ExecutorTerminalFilename))
			var terminal runner.ExecutorTerminal
			if err := json.Unmarshal(terminalData, &terminal); err != nil {
				t.Fatalf("decode executor_terminal.json: %v", err)
			}
			if terminal.Normal {
				t.Fatalf("executor_terminal.json Normal = true, want interrupted: %s", terminalData)
			}
			if terminal.Reason != tc.wantReason {
				t.Fatalf("executor_terminal.json reason = %q, want %q", terminal.Reason, tc.wantReason)
			}
			if terminal.Provider != tc.wantProvider {
				t.Fatalf("executor_terminal.json provider = %q, want %q", terminal.Provider, tc.wantProvider)
			}
			for _, want := range tc.wantMessage {
				if !strings.Contains(string(terminalData), want) {
					t.Fatalf("executor_terminal.json missing detail %q: %s", want, terminalData)
				}
			}
		})
	}
}

func TestInterruptedExecutorStagingFailureStaysInterrupted(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	promptPath, schemaPath := writeDaemonPromptFiles(t)
	claudeBin := writeRawExecutor(t, "claude", "echo partial > daemon-output.txt\n"+
		`printf '%s\n' '{"type":"result","subtype":"error_during_execution","is_error":true,"session_id":"stage-sess"}'`+"\n")
	taskPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, taskPath, repo)
	setLoopBudget(t, taskPath, 3)

	stageFail := func(_ context.Context, _ Options, _, _ string, _ []string) error {
		return context.Canceled
	}
	var supCalls int32
	opts := testDaemonOptions(Options{Root: root, SystemPromptFile: promptPath, JSONSchemaFile: schemaPath, Once: true, MaxConcurrentTasks: 1, Supervisor: "claude", ClaudeBin: claudeBin})
	deps := opts.daemonDependencies()
	deps.stageExecutorOutput = stageFail
	deps.supervisorRunner = countingSupervisor(&supCalls, func(int) supervisor.Verdict {
		t.Errorf("supervisor must not run for an interrupted executor")
		return supervisor.Verdict{Status: "accepted"}
	})
	opts.dependencies = &deps
	_ = Run(context.Background(), opts)

	failed := assertInterruptedFailedTask(t, root, &supCalls)
	last := failed.Attempts[len(failed.Attempts)-1]
	if last.Error.Kind != "executor_interrupted" {
		t.Fatalf("error kind = %q, want executor_interrupted (staging failure must not override interruption)", last.Error.Kind)
	}
	if last.Error.Phase != "executor" {
		t.Fatalf("error phase = %q, want executor", last.Error.Phase)
	}
	foundEvidenceRisk := false
	for _, r := range failed.Risks {
		if strings.HasPrefix(r.ID, "executor-interruption-evidence") {
			foundEvidenceRisk = true
		}
	}
	if !foundEvidenceRisk {
		t.Fatalf("expected secondary staging-failure risk, got risks: %#v", failed.Risks)
	}
}

// CaptureDiffArtifacts reports a snapshot/diff failure in-band as
// DiffArtifacts.Err with a nil function error (distinct from a staging or
// artifact-write error). The interrupted path must promote that into the
// secondary-evidence risk while the interruption stays primary and requeueable.
func TestInterruptedExecutorDiffCaptureFailureStaysInterrupted(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	promptPath, schemaPath := writeDaemonPromptFiles(t)
	claudeBin := writeRawExecutor(t, "claude", "echo changed >> README.md\necho created > new-file.txt\n"+
		`printf '%s\n' '{"type":"result","subtype":"error_during_execution","is_error":true,"session_id":"diff-sess"}'`+"\n")
	taskPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, taskPath, repo)
	setLoopBudget(t, taskPath, 3)

	diffFail := func(_ context.Context, _, _, _ string, _ workspace.Options) (executorflow.DiffArtifacts, error) {
		return executorflow.DiffArtifacts{Err: errors.New("git status: snapshot failed")}, nil
	}
	var supCalls int32
	opts := testDaemonOptions(Options{Root: root, SystemPromptFile: promptPath, JSONSchemaFile: schemaPath, Once: true, MaxConcurrentTasks: 1, Supervisor: "claude", ClaudeBin: claudeBin})
	deps := opts.daemonDependencies()
	deps.captureDiffArtifacts = diffFail
	deps.supervisorRunner = countingSupervisor(&supCalls, func(int) supervisor.Verdict {
		t.Errorf("supervisor must not run for an interrupted executor")
		return supervisor.Verdict{Status: "accepted"}
	})
	opts.dependencies = &deps
	_ = Run(context.Background(), opts)

	failed := assertInterruptedFailedTask(t, root, &supCalls)
	last := failed.Attempts[len(failed.Attempts)-1]
	if last.Error.Kind != "executor_interrupted" {
		t.Fatalf("error kind = %q, want executor_interrupted (diff-capture failure must not override interruption)", last.Error.Kind)
	}
	foundEvidenceRisk := false
	for _, r := range failed.Risks {
		if strings.HasPrefix(r.ID, "executor-interruption-evidence") && strings.Contains(r.Detail, "snapshot failed") {
			foundEvidenceRisk = true
		}
	}
	if !foundEvidenceRisk {
		t.Fatalf("expected secondary diff-capture-failure risk, got risks: %#v", failed.Risks)
	}

	worktree := taskWorktreePath(repo, failed.Worktree.Path)
	if data, err := os.ReadFile(filepath.Join(worktree, "new-file.txt")); err != nil || !strings.Contains(string(data), "created") {
		t.Fatalf("untracked change not retained in worktree: %v %q", err, data)
	}

	failedPath := filepath.Join(root, "tasks", "failed", "task.yaml")
	if _, err := task.Requeue(failedPath, task.RequeueOptions{Root: root, Reason: "resolved interruption"}); err != nil {
		t.Fatalf("requeue: %v", err)
	}
	rerun := testDaemonOptions(Options{Root: root, SystemPromptFile: promptPath, JSONSchemaFile: schemaPath, Once: true, MaxConcurrentTasks: 1, Supervisor: "claude", ClaudeBin: claudeBin})
	_ = Run(context.Background(), rerun)

	reuseFound := false
	workspaces, _ := filepath.Glob(filepath.Join(root, "runs", "*", runartifact.WorkspaceFilename))
	for _, path := range workspaces {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var prepared struct {
			WorktreeReused  bool   `json:"worktree_reused"`
			StatusPorcelain string `json:"status_porcelain"`
		}
		if err := json.Unmarshal(data, &prepared); err != nil {
			continue
		}
		if prepared.WorktreeReused && strings.Contains(prepared.StatusPorcelain, "new-file.txt") {
			reuseFound = true
		}
	}
	if !reuseFound {
		t.Fatal("no reused-worktree evidence with retained change found after requeue")
	}
}

func TestNormalTerminalInvalidResultStillReviewedAndRetried(t *testing.T) {
	claudeBin := writeFakeClaude(t, `echo change > daemon-output.txt
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"result":"not-valid-json","session_id":"sess-ac2"}'`+"\n")
	assertInvalidResultReviewedAndRetried(t, "claude", claudeBin, "")
}

func TestGrokEndTurnInvalidResultStillReviewedAndRetried(t *testing.T) {
	grokBin := writeRawExecutor(t, "grok", `echo change > grok-output.txt
printf '%s\n' '{"text":"garbage not a result","stopReason":"EndTurn","sessionId":"grok-ac2"}'`+"\n")
	claudeBin := writeFakeClaude(t, "exit 1\n")
	assertInvalidResultReviewedAndRetried(t, "grok", claudeBin, grokBin)
}

func TestInterruptionPreservesWorkspaceAndRequeueReuses(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	promptPath, schemaPath := writeDaemonPromptFiles(t)
	// README.md is committed by initDaemonGitRepo; new-file.txt is untracked.
	claudeBin := writeFakeClaude(t, "echo changed >> README.md\necho created > new-file.txt\nexit 1\n")
	taskPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, taskPath, repo)

	opts := testDaemonOptions(Options{Root: root, SystemPromptFile: promptPath, JSONSchemaFile: schemaPath, Once: true, MaxConcurrentTasks: 1, Supervisor: "claude", ClaudeBin: claudeBin})
	_ = Run(context.Background(), opts)

	failedPath := filepath.Join(root, "tasks", "failed", "task.yaml")
	failed, err := task.Load(failedPath)
	if err != nil {
		t.Fatalf("failed task missing: %v", err)
	}

	statusData := mustReadSingleGlob(t, filepath.Join(root, "runs", "*", "attempt-1", runartifact.GitStatusFilename))
	if !strings.Contains(string(statusData), "README.md") || !strings.Contains(string(statusData), "new-file.txt") {
		t.Fatalf("git status evidence missing changes: %s", statusData)
	}

	worktree := taskWorktreePath(repo, failed.Worktree.Path)
	if data, err := os.ReadFile(filepath.Join(worktree, "new-file.txt")); err != nil || !strings.Contains(string(data), "created") {
		t.Fatalf("untracked change not retained in worktree: %v %q", err, data)
	}
	if data, err := os.ReadFile(filepath.Join(worktree, "README.md")); err != nil || !strings.Contains(string(data), "changed") {
		t.Fatalf("tracked change not retained in worktree: %v %q", err, data)
	}

	if _, err := task.Requeue(failedPath, task.RequeueOptions{Root: root, Reason: "resolved interruption"}); err != nil {
		t.Fatalf("requeue: %v", err)
	}
	_ = Run(context.Background(), opts)

	reuseFound := false
	workspaces, _ := filepath.Glob(filepath.Join(root, "runs", "*", runartifact.WorkspaceFilename))
	for _, path := range workspaces {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var prepared struct {
			WorktreeReused  bool   `json:"worktree_reused"`
			Dirty           bool   `json:"dirty"`
			StatusPorcelain string `json:"status_porcelain"`
		}
		if err := json.Unmarshal(data, &prepared); err != nil {
			continue
		}
		if prepared.WorktreeReused {
			reuseFound = true
			if !prepared.Dirty {
				t.Fatalf("reused worktree evidence must be dirty: %s", data)
			}
			if !strings.Contains(prepared.StatusPorcelain, "new-file.txt") {
				t.Fatalf("reused worktree must still show retained change: %s", data)
			}
		}
	}
	if !reuseFound {
		t.Fatal("no reused-worktree evidence found after requeue")
	}
}

func assertInterruptedFailedTask(t *testing.T, root string, supCalls *int32) task.Task {
	t.Helper()
	if got := atomic.LoadInt32(supCalls); got != 0 {
		t.Fatalf("supervisor calls = %d, want 0", got)
	}
	failed, err := task.Load(filepath.Join(root, "tasks", "failed", "task.yaml"))
	if err != nil {
		t.Fatalf("failed task missing: %v", err)
	}
	if failed.Status != "failed" {
		t.Fatalf("status = %q, want failed", failed.Status)
	}
	if len(failed.Attempts) != 1 {
		t.Fatalf("attempts = %d, want exactly 1 (%#v)", len(failed.Attempts), failed.Attempts)
	}
	only := failed.Attempts[0]
	if only.SupervisorVerdict != "not_reviewed" {
		t.Fatalf("supervisor_verdict = %q, want not_reviewed", only.SupervisorVerdict)
	}
	if only.Error == nil || only.Error.Phase != "executor" {
		t.Fatalf("attempt error = %#v, want executor phase", only.Error)
	}
	assertGlobCount(t, filepath.Join(root, "runs", "*", "attempt-1", runartifact.ExecutorTerminalFilename), 1)
	if matches, _ := filepath.Glob(filepath.Join(root, "runs", "*", "attempt-2")); len(matches) != 0 {
		t.Fatalf("attempt-2 must not exist: %v", matches)
	}
	return failed
}

func assertInvalidResultReviewedAndRetried(t *testing.T, cli, claudeBin, executorBin string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	promptPath, schemaPath := writeDaemonPromptFiles(t)
	taskPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, taskPath, repo)
	setExecutorCLI(t, taskPath, cli)
	setLoopBudget(t, taskPath, 3)

	opts := Options{Root: root, SystemPromptFile: promptPath, JSONSchemaFile: schemaPath, Once: true, MaxConcurrentTasks: 1, Supervisor: "claude", ClaudeBin: claudeBin}
	if cli == "grok" {
		opts.GrokBin = executorBin
	}

	var supCalls int32
	opts = testDaemonOptions(opts)
	opts = withSupervisorSeam(opts, countingSupervisor(&supCalls, func(attempt int) supervisor.Verdict {
		if attempt == 1 {
			return supervisor.Verdict{Status: "needs_revision", Summary: "fix invalid result", Findings: []string{"Return valid structured JSON."}}
		}
		return supervisor.Verdict{Status: "accepted", Summary: "accepted after revision"}
	}))
	if err := Run(context.Background(), opts); err != nil {
		t.Fatalf("run: %v", err)
	}

	if got := atomic.LoadInt32(&supCalls); got != 2 {
		t.Fatalf("supervisor calls = %d, want 2 (invalid result reviewed and needs_revision retried)", got)
	}
	done, err := task.Load(filepath.Join(root, "tasks", "done", "task.yaml"))
	if err != nil {
		t.Fatalf("done task missing: %v", err)
	}
	if done.Status != "accepted" {
		t.Fatalf("status = %q, want accepted", done.Status)
	}
	if len(done.Attempts) != 2 {
		t.Fatalf("attempts = %d, want 2", len(done.Attempts))
	}
}

func setExecutorCLI(t *testing.T, taskPath, cli string) {
	t.Helper()
	loaded, err := task.Load(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Executor.CLI = cli
	if err := task.Save(taskPath, loaded); err != nil {
		t.Fatal(err)
	}
}
