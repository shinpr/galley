package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shinpr/galley/internal/runner"
	"github.com/shinpr/galley/internal/supervisor"
	"github.com/shinpr/galley/internal/task"
)

func runnerCommandErr(kind runner.CommandErrorKind, err error) error {
	return &runner.CommandError{Kind: kind, Err: err}
}

func TestEvaluateSupervisorRunsOnceAndWritesVerdict(t *testing.T) {
	attemptDir := t.TempDir()
	calls := 0
	runnerForTest := func(_ context.Context, _ Options, _ supervisor.Evidence, artifactDir, _ string) (supervisor.Verdict, error) {
		calls++
		if want := filepath.Join(attemptDir, "supervisor-try-1"); artifactDir != want {
			t.Fatalf("artifactDir got %q, want %q", artifactDir, want)
		}
		return supervisor.Verdict{Status: "accepted", Summary: "ok"}, nil
	}

	opts := Options{Supervisor: "codex", dependencies: &daemonDependencies{supervisorRunner: runnerForTest}}
	verdict, err := evaluateSupervisor(context.Background(), opts, supervisor.Evidence{Task: task.Task{ID: "test"}}, attemptDir, attemptDir)
	if err != nil {
		t.Fatalf("evaluateSupervisor returned error: %v", err)
	}
	if verdict.Status != "accepted" || calls != 1 {
		t.Fatalf("verdict=%#v calls=%d, want one accepted invocation", verdict, calls)
	}
	for _, path := range []string{
		filepath.Join(attemptDir, "supervisor-try-1", "supervisor_verdict.json"),
		filepath.Join(attemptDir, "model_supervisor_verdict.json"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("verdict artifact missing at %s: %v", path, err)
		}
	}
	if _, err := os.Stat(filepath.Join(attemptDir, "supervisor-try-2")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("supervisor-try-2 must not exist, stat error=%v", err)
	}
}

func TestEvaluateSupervisorReturnsInvalidVerdictWithoutRetry(t *testing.T) {
	attemptDir := t.TempDir()
	calls := 0
	wantErr := &supervisor.VerdictContractError{Err: errors.New("missing quality coverage")}
	runnerForTest := func(_ context.Context, _ Options, _ supervisor.Evidence, _, _ string) (supervisor.Verdict, error) {
		calls++
		return supervisor.Verdict{}, wantErr
	}

	opts := Options{Supervisor: "codex", dependencies: &daemonDependencies{supervisorRunner: runnerForTest}}
	_, err := evaluateSupervisor(context.Background(), opts, supervisor.Evidence{Task: task.Task{ID: "test"}}, attemptDir, attemptDir)
	if !errors.Is(err, wantErr) || calls != 1 {
		t.Fatalf("err=%v calls=%d, want original error after one invocation", err, calls)
	}
	data, readErr := os.ReadFile(filepath.Join(attemptDir, "supervisor-try-1", "supervisor_error.json"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["kind"] != "supervisor_invalid_verdict" {
		t.Fatalf("error kind got %v, want supervisor_invalid_verdict", payload["kind"])
	}
	if _, err := os.Stat(filepath.Join(attemptDir, "supervisor-try-2")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("supervisor-try-2 must not exist, stat error=%v", err)
	}
}

func TestEvaluateSupervisorReturnsIdleTimeoutWithoutRetry(t *testing.T) {
	attemptDir := t.TempDir()
	calls := 0
	runnerForTest := func(_ context.Context, _ Options, _ supervisor.Evidence, _, _ string) (supervisor.Verdict, error) {
		calls++
		return supervisor.Verdict{}, runnerCommandErr(runner.CommandErrorIdleTimeout, errors.New("no output"))
	}

	opts := Options{Supervisor: "codex", IdleTimeout: 90 * time.Second, dependencies: &daemonDependencies{supervisorRunner: runnerForTest}}
	_, err := evaluateSupervisor(context.Background(), opts, supervisor.Evidence{Task: task.Task{ID: "test"}}, attemptDir, attemptDir)
	idle, ok := asSupervisorIdleTimeout(err)
	if !ok || calls != 1 {
		t.Fatalf("err=%v calls=%d, want one typed idle-timeout failure", err, calls)
	}
	for _, want := range []string{"supervisor=codex", "idle_timeout=1m30s", "not the task execution_policy.timeout_ms expiring", "Requeue the task"} {
		if !strings.Contains(idle.attemptErrorMessage(), want) {
			t.Fatalf("attempt error message %q must contain %q", idle.attemptErrorMessage(), want)
		}
	}
	if strings.Contains(idle.attemptErrorMessage(), "tries=") || strings.Contains(idle.attemptErrorMessage(), "retry") {
		t.Fatalf("single-invocation error must not describe automatic retries: %q", idle.attemptErrorMessage())
	}
	if _, err := os.Stat(filepath.Join(attemptDir, "supervisor-try-2")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("supervisor-try-2 must not exist, stat error=%v", err)
	}
}
