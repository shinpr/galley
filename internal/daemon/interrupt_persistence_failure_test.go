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

// claudeAPIErrorInterruptionBody is a Claude executor body that changes the
// workspace and emits an explicit provider API-error result event, so the
// attempt is an interruption regardless of the appended success terminal.
const claudeAPIErrorInterruptionBody = "echo change > daemon-output.txt\n" +
	`echo '{"type":"result","subtype":"error_during_execution","is_error":true,"error":"provider outage"}'` + "\n"

func runClaudeInterruptionWithDeps(t *testing.T, body string, deps *daemonDependencies) (task.Task, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	promptPath, schemaPath := writeDaemonPromptFiles(t)
	claudeBin := writeFakeClaude(t, body)
	taskPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, taskPath, repo)
	setLoopBudget(t, taskPath, 3)
	_ = runTestDaemon(context.Background(), Options{
		Root: root, SystemPromptFile: promptPath, JSONSchemaFile: schemaPath,
		Once: true, MaxConcurrentTasks: 1, Supervisor: "claude", ClaudeBin: claudeBin,
		dependencies: deps,
	})
	return mustLoadFailedTask(t, root), root
}

func assertSingleInterruption(t *testing.T, failed task.Task, root string) {
	t.Helper()
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
	assertGlobCount(t, filepath.Join(root, "runs", "*", "attempt-1", "supervisor_verdict.json"), 0)
	assertGlobCount(t, filepath.Join(root, "runs", "*", "attempt-2"), 0)
}

func assertRiskDetailContains(t *testing.T, failed task.Task, want string) {
	t.Helper()
	for _, r := range failed.Risks {
		if strings.Contains(r.Detail, want) {
			return
		}
	}
	t.Fatalf("expected a risk disclosing %q: %#v", want, failed.Risks)
}

func TestInterruptionSurvivesRunResultWriteFailure(t *testing.T) {
	deps := &daemonDependencies{
		writeAttemptArtifact: func(path string, value any) error {
			if strings.HasSuffix(path, runartifact.RunResultFilename) {
				return fmt.Errorf("simulated run_result write failure")
			}
			return writeJSON(path, value)
		},
	}
	failed, root := runClaudeInterruptionWithDeps(t, claudeAPIErrorInterruptionBody, deps)
	assertSingleInterruption(t, failed, root)
	assertGlobCount(t, filepath.Join(root, "runs", "*", "attempt-1", runartifact.RunResultFilename), 0)
	assertGlobCount(t, filepath.Join(root, "runs", "*", "attempt-1", runartifact.ExecutorTerminalFilename), 1)
	assertRiskDetailContains(t, failed, "run_result.json could not be written")
}

func TestInterruptionSurvivesTerminalWriteFailure(t *testing.T) {
	deps := &daemonDependencies{
		writeAttemptArtifact: func(path string, value any) error {
			if strings.HasSuffix(path, runartifact.ExecutorTerminalFilename) {
				return fmt.Errorf("simulated executor_terminal write failure")
			}
			return writeJSON(path, value)
		},
	}
	failed, root := runClaudeInterruptionWithDeps(t, claudeAPIErrorInterruptionBody, deps)
	assertSingleInterruption(t, failed, root)
	assertGlobCount(t, filepath.Join(root, "runs", "*", "attempt-1", runartifact.ExecutorTerminalFilename), 0)
	assertGlobCount(t, filepath.Join(root, "runs", "*", "attempt-1", runartifact.RunResultFilename), 1)
	assertRiskDetailContains(t, failed, "executor_terminal.json could not be written")
}

func TestInterruptionSurvivesProviderAndResultMetadataWriteFailure(t *testing.T) {
	t.Run("grok provider metadata", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), ".agent-workflow")
		repo := initDaemonGitRepo(t)
		promptPath, schemaPath := writeDaemonPromptFiles(t)
		claudeBin := writeFakeClaude(t, "")
		// Non-EndTurn stopReason is an interruption; the provider metadata write
		// is injected to fail.
		grokBin := writeFakeCommand(t, "grok", "cat >/dev/null 2>&1 || true\nprintf '%s\\n' '{\"text\":\"{}\",\"stopReason\":\"Cancelled\",\"sessionId\":\"g1\"}'\n")
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
		setLoopBudget(t, taskPath, 3)
		deps := &daemonDependencies{
			writeProviderMetadata: func(path string, data []byte) error {
				if strings.HasSuffix(path, runartifact.GrokCompletionMetadataFilename) {
					return fmt.Errorf("simulated grok metadata write failure")
				}
				return os.WriteFile(path, data, 0o600)
			},
		}
		_ = runTestDaemon(context.Background(), Options{
			Root: root, SystemPromptFile: promptPath, JSONSchemaFile: schemaPath,
			Once: true, MaxConcurrentTasks: 1, Supervisor: "claude", ClaudeBin: claudeBin, GrokBin: grokBin,
			dependencies: deps,
		})
		failed := mustLoadFailedTask(t, root)
		assertSingleInterruption(t, failed, root)
		assertRiskDetailContains(t, failed, "grok_completion.json could not be written")
	})

	t.Run("executor result metadata", func(t *testing.T) {
		validResult := `{"status":"completed","summary":"done","files_modified":["daemon-output.txt"],"acceptance_criteria":[],"verification":[],"scope_expansions":[],"decisions":[],"risks":[]}`
		// A parseable result plus an explicit API-error terminal: the result write
		// is attempted and injected to fail while the attempt stays an interruption.
		body := "echo change > daemon-output.txt\n" +
			"echo '" + validResult + "'\n" +
			`echo '{"type":"result","subtype":"error_during_execution","is_error":true,"error":"provider outage"}'` + "\n"
		deps := &daemonDependencies{
			writeAttemptArtifact: func(path string, value any) error {
				if strings.HasSuffix(path, runartifact.ExecutorResultFilename) {
					return fmt.Errorf("simulated executor_result write failure")
				}
				return writeJSON(path, value)
			},
		}
		failed, root := runClaudeInterruptionWithDeps(t, body, deps)
		assertSingleInterruption(t, failed, root)
		assertRiskDetailContains(t, failed, "executor_result.json could not be written")
	})
}

func TestInterruptionSurvivesStagingOnlyFailure(t *testing.T) {
	deps := &daemonDependencies{
		stageExecutorOutput: func(_ context.Context, _ Options, _, _ string, _ []string) error {
			return fmt.Errorf("git add -A (review staging) failed: simulated index lock")
		},
	}
	// Modify a tracked file so the unstaged diff captures the change even though
	// staging failed (an untracked file would need staging to appear).
	body := "echo RESUMED >> README.md\n" +
		`echo '{"type":"result","subtype":"error_during_execution","is_error":true,"error":"provider outage"}'` + "\n"
	failed, root := runClaudeInterruptionWithDeps(t, body, deps)
	assertSingleInterruption(t, failed, root)
	diff := string(mustReadSingleGlob(t, filepath.Join(root, "runs", "*", "attempt-1", runartifact.DiffPatchFilename)))
	if !strings.Contains(diff, "RESUMED") {
		t.Fatalf("diff.patch must retain the captured change: %q", diff)
	}
	assertGlobCount(t, filepath.Join(root, "runs", "*", "attempt-1", runartifact.GitStatusFilename), 1)
	assertRiskDetailContains(t, failed, "review staging failed")
}
