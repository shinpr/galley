package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shinpr/galley/internal/runartifact"
	"github.com/shinpr/galley/internal/task"
)

func TestGrokExecutorProcessFailuresKeepGenericDaemonClassifications(t *testing.T) {
	tests := []struct {
		name, body, wantKind string
		missingBinary        bool
		idle, total          time.Duration
	}{
		{name: "start failure", missingBinary: true, wantKind: "executor_failed"},
		{name: "non-zero exit", body: "exit 7\n", wantKind: "executor_failed"},
		{name: "idle timeout", body: "sleep 5\n", idle: 100 * time.Millisecond, wantKind: "idle_timeout"},
		{name: "total timeout", body: "sleep 5\n", total: 100 * time.Millisecond, wantKind: "timed_out"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assertGrokFailureClassification(t, grokFailureCase{
				Body: tc.body, WantKind: tc.wantKind, MissingBinary: tc.missingBinary,
				Idle: tc.idle, Total: tc.total,
			})
		})
	}
}

// grokFailureCase is one grok executor process failure and the attempt error
// kind the daemon must record for it.
type grokFailureCase struct {
	Body          string
	WantKind      string
	MissingBinary bool
	Idle          time.Duration
	Total         time.Duration
}

func assertGrokFailureClassification(t *testing.T, tc grokFailureCase) {
	t.Helper()
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	promptPath, schemaPath := writeDaemonPromptFiles(t)
	grokBin := filepath.Join(t.TempDir(), "missing-grok")
	if !tc.MissingBinary {
		if err := os.WriteFile(grokBin, []byte("#!/bin/sh\n"+tc.Body), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	taskPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o750); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, taskPath, repo)
	loaded, err := task.Load(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Executor.CLI = "grok"
	if tc.Total > 0 {
		loaded.ExecutionPolicy.TimeoutMS = int(tc.Total / time.Millisecond)
	}
	if err := task.Save(taskPath, loaded); err != nil {
		t.Fatal(err)
	}
	setLoopBudget(t, taskPath, 1)
	_ = runTestDaemon(context.Background(), Options{Root: root, SystemPromptFile: promptPath, JSONSchemaFile: schemaPath, Once: true, MaxConcurrentTasks: 1, Supervisor: "claude", ClaudeBin: writeFakeClaude(t, "exit 1\n"), GrokBin: grokBin, IdleTimeout: tc.Idle})
	failed, err := task.Load(filepath.Join(root, "tasks", "failed", "task.yaml"))
	if err != nil {
		t.Fatalf("failed task missing: %v", err)
	}
	if len(failed.Attempts) == 0 || failed.Attempts[0].Error == nil || failed.Attempts[0].Error.Kind != tc.WantKind {
		t.Fatalf("attempt classification = %#v; want %s", failed.Attempts, tc.WantKind)
	}
	assertGlobCount(t, filepath.Join(root, "runs", "*", "attempt-1", runartifact.RunResultFilename), 1)
}
