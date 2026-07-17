package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shinpr/galley/internal/runartifact"
	"github.com/shinpr/galley/internal/task"
)

func TestGrokExecutorRuntimeFailuresInterruptBeforeSupervisor(t *testing.T) {
	tests := []struct {
		name, body, wantReason string
		missingBinary          bool
		idle, total            time.Duration
	}{
		{name: "start failure", missingBinary: true, wantReason: "start_failed"},
		{name: "non-zero exit", body: "exit 7\n", wantReason: "exit_nonzero"},
		{name: "idle timeout", body: "sleep 5\n", idle: 100 * time.Millisecond, wantReason: "idle_timeout"},
		{name: "total timeout", body: "sleep 5\n", total: 100 * time.Millisecond, wantReason: "timed_out"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), ".agent-workflow")
			repo := initDaemonGitRepo(t)
			promptPath, schemaPath := writeDaemonPromptFiles(t)
			grokBin := filepath.Join(t.TempDir(), "missing-grok")
			if !tc.missingBinary {
				if err := os.WriteFile(grokBin, []byte("#!/bin/sh\n"+tc.body), 0o700); err != nil {
					t.Fatal(err)
				}
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
			loaded.Executor.CLI = "grok"
			if tc.total > 0 {
				loaded.ExecutionPolicy.TimeoutMS = int(tc.total / time.Millisecond)
			}
			if err := task.Save(taskPath, loaded); err != nil {
				t.Fatal(err)
			}
			// loop_budget > 1 proves the interruption never starts attempt 2.
			setLoopBudget(t, taskPath, 3)
			// A supervisor that fails loudly if ever invoked; an interruption
			// must not call it at all.
			supervisorMarker := filepath.Join(t.TempDir(), "supervisor.called")
			claudeBin := writeFakeClaude(t, "echo called >> "+supervisorMarker+"\nexit 1\n")
			_ = runTestDaemon(context.Background(), Options{Root: root, SystemPromptFile: promptPath, JSONSchemaFile: schemaPath, Once: true, MaxConcurrentTasks: 1, Supervisor: "claude", ClaudeBin: claudeBin, GrokBin: grokBin, IdleTimeout: tc.idle})
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
				t.Fatalf("supervisor_verdict = %q; want %q", attempt.SupervisorVerdict, task.AttemptVerdictNotReviewed)
			}
			if attempt.Error == nil || attempt.Error.Kind != task.AttemptKindExecutorInterrupted {
				t.Fatalf("attempt error = %#v; want kind %q", attempt.Error, task.AttemptKindExecutorInterrupted)
			}
			if !strings.Contains(attempt.Error.Message, tc.wantReason) {
				t.Fatalf("interruption message = %q; want diagnostic reason %q", attempt.Error.Message, tc.wantReason)
			}
			if _, err := os.Stat(supervisorMarker); err == nil {
				t.Fatal("supervisor was invoked for an interrupted executor attempt")
			}
			assertGlobCount(t, filepath.Join(root, "runs", "*", "attempt-1", runartifact.RunResultFilename), 1)
			assertGlobCount(t, filepath.Join(root, "runs", "*", "attempt-1", runartifact.ExecutorTerminalFilename), 1)
			assertGlobCount(t, filepath.Join(root, "runs", "*", "attempt-1", "supervisor_verdict.json"), 0)
			assertGlobCount(t, filepath.Join(root, "runs", "*", "attempt-2"), 0)
		})
	}
}
