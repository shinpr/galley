package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shinpr/galley/internal/supervisor"
	"github.com/shinpr/galley/internal/task"
)

// TestSupervisorRetryRecoversAfterStallOnSecondTry exercises AC-001: when the
// first supervisor evaluation fails with an idle timeout, evaluateSupervisorWithRetry
// must retry the same evaluation under the same executor attempt directory,
// write per-try evidence under supervisor-try-N subdirectories, recover when
// the second try succeeds, and not consume additional executor attempts.
func TestSupervisorRetryRecoversAfterStallOnSecondTry(t *testing.T) {
	attemptDir := t.TempDir()
	originalRunner := supervisorRunner
	t.Cleanup(func() { supervisorRunner = originalRunner })

	calls := 0
	supervisorRunner = func(ctx context.Context, opts Options, evidence supervisor.Evidence, tryDir, workDir string) (supervisor.Verdict, error) {
		calls++
		// Each invocation must receive a distinct supervisor-try-N directory so
		// retry evidence does not overwrite prior output (R1).
		want := filepath.Join(attemptDir, fmt.Sprintf("supervisor-try-%d", calls))
		if tryDir != want {
			t.Fatalf("try %d: tryDir got %q want %q", calls, tryDir, want)
		}
		if _, err := os.Stat(tryDir); err != nil {
			t.Fatalf("try %d: tryDir missing: %v", calls, err)
		}
		if calls == 1 {
			return supervisor.Verdict{}, errors.New("command produced no output for 1s (idle timeout)")
		}
		return supervisor.Verdict{Status: "accepted", Summary: "ok"}, nil
	}

	verdict, err := evaluateSupervisorWithRetry(context.Background(), Options{}, supervisor.Evidence{Task: task.Task{ID: "test"}}, attemptDir, attemptDir)
	if err != nil {
		t.Fatalf("evaluateSupervisorWithRetry returned error: %v", err)
	}
	if verdict.Status != "accepted" {
		t.Fatalf("verdict.Status got %q, want accepted", verdict.Status)
	}
	if calls != 2 {
		t.Fatalf("supervisor invocations got %d, want 2 (initial + 1 retry)", calls)
	}

	// AC-001 retry evidence: the first try's error JSON must be inspectable and
	// the second try's verdict JSON must be present under the same executor
	// attempt directory.
	errPath := filepath.Join(attemptDir, "supervisor-try-1", "supervisor_error.json")
	if _, err := os.Stat(errPath); err != nil {
		t.Fatalf("supervisor-try-1/supervisor_error.json missing: %v", err)
	}
	data, err := os.ReadFile(errPath)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("decode supervisor_error.json: %v", err)
	}
	if payload["kind"] != "idle_timeout" {
		t.Fatalf("supervisor-try-1 kind got %v, want idle_timeout", payload["kind"])
	}
	if _, err := os.Stat(filepath.Join(attemptDir, "supervisor-try-2", "supervisor_verdict.json")); err != nil {
		t.Fatalf("supervisor-try-2/supervisor_verdict.json missing: %v", err)
	}
	// The downstream top-level evidence path that the loop and existing tests
	// rely on must also be present after a recovered retry.
	if _, err := os.Stat(filepath.Join(attemptDir, "model_supervisor_verdict.json")); err != nil {
		t.Fatalf("model_supervisor_verdict.json missing: %v", err)
	}
	// supervisor-try-3 must not exist: retries stop at the first success.
	if _, err := os.Stat(filepath.Join(attemptDir, "supervisor-try-3")); err == nil {
		t.Fatal("supervisor-try-3 should not exist after retry success")
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected stat error: %v", err)
	}
}

// TestSupervisorRetryExhaustedReturnsClassifiedFailure exercises AC-002: when
// the supervisor evaluation keeps stalling, evaluateSupervisorWithRetry must
// exhaust the fixed retry budget, preserve evidence for each retry under
// supervisor-try-N, and surface a classified supervisor error. The follow-up
// appendSupervisorFailureAttempt path is what stamps the attempt-level error
// phase/kind that runSupervisorLoop persists into the task file before
// taskstate.FailMove marks the task as failed, so we verify both halves.
func TestSupervisorRetryExhaustedReturnsClassifiedFailure(t *testing.T) {
	attemptDir := t.TempDir()
	originalRunner := supervisorRunner
	t.Cleanup(func() { supervisorRunner = originalRunner })

	calls := 0
	supervisorRunner = func(ctx context.Context, opts Options, evidence supervisor.Evidence, tryDir, workDir string) (supervisor.Verdict, error) {
		calls++
		return supervisor.Verdict{}, errors.New("command produced no output for 1s (idle timeout)")
	}

	_, err := evaluateSupervisorWithRetry(context.Background(), Options{}, supervisor.Evidence{Task: task.Task{ID: "test"}}, attemptDir, attemptDir)
	if err == nil {
		t.Fatal("expected exhausted retries to return an error")
	}
	if !strings.Contains(err.Error(), "after 3 tries") {
		t.Fatalf("error message got %q, want it to mention the total try count", err.Error())
	}
	if calls != supervisorTotalAttempts {
		t.Fatalf("supervisor invocations got %d, want %d", calls, supervisorTotalAttempts)
	}

	// Each retry must have its own evidence directory and error JSON (R1).
	for i := 1; i <= supervisorTotalAttempts; i++ {
		path := filepath.Join(attemptDir, fmt.Sprintf("supervisor-try-%d", i), "supervisor_error.json")
		if _, statErr := os.Stat(path); statErr != nil {
			t.Fatalf("supervisor-try-%d/supervisor_error.json missing: %v", i, statErr)
		}
	}
	// The top-level supervisor verdict should not exist after total exhaustion.
	if _, statErr := os.Stat(filepath.Join(attemptDir, "model_supervisor_verdict.json")); statErr == nil {
		t.Fatal("model_supervisor_verdict.json should not be written when supervisor evaluation fails")
	}

	// appendSupervisorFailureAttempt is the persistence path runSupervisorLoop
	// calls when evaluateSupervisorWithRetry returns this error. Run it here to
	// confirm the attempt error phase/kind that downstream readers see.
	loaded := &task.Task{ID: "test"}
	appendSupervisorFailureAttempt(loaded, attemptOutcome{}, err, attemptDir)
	if len(loaded.Attempts) != 1 {
		t.Fatalf("attempts got %d, want 1", len(loaded.Attempts))
	}
	attempt := loaded.Attempts[0]
	if attempt.Error == nil {
		t.Fatal("attempt error is nil")
	}
	if attempt.Error.Phase != "supervisor" {
		t.Fatalf("attempt error phase got %q, want supervisor", attempt.Error.Phase)
	}
	// AC2: an exhausted supervisor idle timeout records the distinct
	// supervisor_idle_timeout kind, not the generic idle_timeout kind.
	if attempt.Error.Kind != "supervisor_idle_timeout" {
		t.Fatalf("attempt error kind got %q, want supervisor_idle_timeout", attempt.Error.Kind)
	}
	if attempt.Error.ArtifactDir != attemptDir {
		t.Fatalf("attempt error artifact dir got %q, want %q", attempt.Error.ArtifactDir, attemptDir)
	}
	// AC2: the attempt error message carries the try count so the failed task
	// YAML is self-describing without daemon logs.
	if !strings.Contains(attempt.Error.Message, "tries=3/3") {
		t.Fatalf("attempt error message %q must report the try count", attempt.Error.Message)
	}
	// AC4: the message must not blame the task execution_policy.timeout_ms.
	if !strings.Contains(attempt.Error.Message, "not the task execution_policy.timeout_ms expiring") {
		t.Fatalf("attempt error message %q must disambiguate from task timeout_ms", attempt.Error.Message)
	}
	// AC6: no needs_revision or accepted verdict is recorded for the exhausted
	// idle-timeout attempt because no supervisor verdict was produced.
	if attempt.SupervisorVerdict == "needs_revision" || attempt.SupervisorVerdict == "accepted" {
		t.Fatalf("attempt supervisor verdict got %q, want an infrastructure kind", attempt.SupervisorVerdict)
	}
	if attempt.SupervisorVerdict != "supervisor_idle_timeout" {
		t.Fatalf("attempt supervisor verdict got %q, want supervisor_idle_timeout", attempt.SupervisorVerdict)
	}
}

func TestSupervisorRetryMixedStallsDoNotReportSupervisorIdleTimeout(t *testing.T) {
	attemptDir := t.TempDir()
	originalRunner := supervisorRunner
	t.Cleanup(func() { supervisorRunner = originalRunner })

	stalls := []error{
		context.DeadlineExceeded,
		errors.New("supervisor process did not exit after cancellation"),
		errors.New("command produced no output for 1s (idle timeout)"),
	}
	calls := 0
	supervisorRunner = func(ctx context.Context, opts Options, evidence supervisor.Evidence, tryDir, workDir string) (supervisor.Verdict, error) {
		calls++
		return supervisor.Verdict{}, stalls[calls-1]
	}

	_, err := evaluateSupervisorWithRetry(context.Background(), Options{}, supervisor.Evidence{Task: task.Task{ID: "test"}}, attemptDir, attemptDir)
	if err == nil {
		t.Fatal("expected exhausted mixed stalls to return an error")
	}
	if calls != supervisorTotalAttempts {
		t.Fatalf("supervisor invocations got %d, want %d", calls, supervisorTotalAttempts)
	}
	if _, ok := asSupervisorIdleTimeout(err); ok {
		t.Fatalf("mixed stall causes must not be reported as supervisor_idle_timeout: %v", err)
	}

	loaded := &task.Task{ID: "test"}
	appendSupervisorFailureAttempt(loaded, attemptOutcome{}, err, attemptDir)
	if len(loaded.Attempts) != 1 {
		t.Fatalf("attempts got %d, want 1", len(loaded.Attempts))
	}
	attempt := loaded.Attempts[0]
	if attempt.Error == nil {
		t.Fatal("attempt error is nil")
	}
	if attempt.Error.Kind == supervisorIdleTimeoutKind {
		t.Fatalf("attempt error kind got %q, want generic stall classification for mixed causes", attempt.Error.Kind)
	}
	if strings.Contains(attempt.Error.Message, "killed on every try") {
		t.Fatalf("mixed stall error message must not claim every try was killed by idle timeout: %q", attempt.Error.Message)
	}
}

// TestSupervisorIdleTimeoutErrorReporting exercises AC2/AC3/AC4: the typed
// supervisorIdleTimeoutError produces a self-describing attempt-error message
// and the exact one-line daemon log shape, and never describes the failure as
// the task execution_policy.timeout_ms expiring.
func TestSupervisorIdleTimeoutErrorReporting(t *testing.T) {
	idle := &supervisorIdleTimeoutError{
		Supervisor:  "codex",
		IdleTimeout: 90 * time.Second,
		Tries:       supervisorTotalAttempts,
		MaxTries:    supervisorTotalAttempts,
		Err:         errors.New("codex supervisor failed: command produced no output for 1m30s (idle timeout)"),
	}

	// AC3: exact one-line daemon log shape.
	wantLog := "galley: task task-xyz failed: supervisor_idle_timeout (supervisor=codex idle_timeout=1m30s tries=3/3; requeue or adjust daemon settings)"
	if got := idle.logLine("task-xyz"); got != wantLog {
		t.Fatalf("log line got %q, want %q", got, wantLog)
	}

	// AC2: the attempt-error message names the phase cause, supervisor adapter,
	// idle-timeout duration, and try count.
	msg := idle.attemptErrorMessage()
	for _, want := range []string{"supervisor idle timeout", "supervisor=codex", "idle_timeout=1m30s", "tries=3/3"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("attempt error message %q must contain %q", msg, want)
		}
	}
	// AC4: the message points operators away from task-timeout wording.
	if !strings.Contains(msg, "not the task execution_policy.timeout_ms expiring") {
		t.Fatalf("attempt error message %q must disambiguate from task timeout_ms", msg)
	}
	// AC5: the message states the next action.
	if !strings.Contains(msg, "Requeue the task") {
		t.Fatalf("attempt error message %q must state the next action", msg)
	}

	// asSupervisorIdleTimeout must unwrap the typed error through fmt wrapping.
	wrapped := fmt.Errorf("daemon run: %w", error(idle))
	if _, ok := asSupervisorIdleTimeout(wrapped); !ok {
		t.Fatal("asSupervisorIdleTimeout must detect a wrapped supervisorIdleTimeoutError")
	}
	// A generic supervisor failure must not be classified as an idle timeout.
	if _, ok := asSupervisorIdleTimeout(errors.New("supervisor evaluation failed after 3 tries: boom")); ok {
		t.Fatal("asSupervisorIdleTimeout must not match a generic supervisor failure")
	}
}
