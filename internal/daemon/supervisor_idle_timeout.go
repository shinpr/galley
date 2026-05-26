package daemon

import (
	"errors"
	"fmt"
	"time"

	"github.com/shinpr/galley/internal/runner"
)

// supervisorIdleTimeoutKind is the distinct attempt-error kind recorded when a
// task fails because the built-in supervisor idle timeout exhausted its
// retries. It is intentionally separate from the generic "idle_timeout" kind
// (used for a single executor or supervisor try) so operators and
// `galley task show` can tell a supervisor watchdog failure apart from an
// executor idle timeout or a task total-timeout expiry.
const supervisorIdleTimeoutKind = "supervisor_idle_timeout"

// supervisorIdleTimeoutError reports that the built-in supervisor subprocess
// produced no stdout/stderr for the configured --idle-timeout on every retry
// and was killed by the idle-output watchdog. evaluateSupervisorWithRetry
// returns it instead of a plain wrapped error so the failure-reporting paths
// (attempt error, daemon log line, `galley task show`) can describe the
// failure as a supervisor watchdog timeout rather than the task's
// execution_policy.timeout_ms expiring.
type supervisorIdleTimeoutError struct {
	// Supervisor is the built-in supervisor adapter name (codex or claude).
	Supervisor string
	// IdleTimeout is the resolved daemon --idle-timeout watchdog duration.
	IdleTimeout time.Duration
	// Tries is the number of supervisor evaluations actually run (the initial
	// try plus retries); MaxTries is the fixed per-attempt supervisor budget.
	Tries    int
	MaxTries int
	// Err is the last underlying idle-timeout error from the supervisor runner.
	Err error
}

func (e *supervisorIdleTimeoutError) Error() string {
	return fmt.Sprintf(
		"supervisor evaluation failed after %d tries: supervisor %s produced no output and was killed by the idle-timeout watchdog: %v",
		e.MaxTries, e.supervisorName(), e.Err,
	)
}

// Unwrap exposes the underlying runner error so errors.Is/errors.As callers
// keep working across the typed wrapper.
func (e *supervisorIdleTimeoutError) Unwrap() error { return e.Err }

// supervisorName returns a stable, non-empty label for the supervisor adapter.
func (e *supervisorIdleTimeoutError) supervisorName() string {
	if e.Supervisor == "" {
		return "unknown"
	}
	return e.Supervisor
}

// attemptErrorMessage builds the operator-facing message persisted on the
// failed task attempt (task.AttemptError.Message) and surfaced by
// `galley task show`. It names the supervisor adapter, the idle-timeout
// duration, and the try count, explicitly distinguishes the failure from the
// task execution_policy.timeout_ms expiring, and states the next action.
func (e *supervisorIdleTimeoutError) attemptErrorMessage() string {
	return fmt.Sprintf(
		"supervisor idle timeout: the built-in supervisor subprocess produced no output for the idle-timeout watchdog and was killed on every try "+
			"(supervisor=%s idle_timeout=%s tries=%d/%d). This is a supervisor watchdog failure, not the task execution_policy.timeout_ms expiring. "+
			"Requeue the task, or adjust the daemon --idle-timeout or --supervisor settings.",
		e.supervisorName(), e.IdleTimeout, e.Tries, e.MaxTries,
	)
}

// logLine builds the one-line daemon stderr report for an exhausted supervisor
// idle timeout. It keeps the existing `galley: task <id> ...` log tone so
// daemon logs stay scannable while the fuller explanation lives in the attempt
// error message and `galley task show`.
func (e *supervisorIdleTimeoutError) logLine(taskID string) string {
	return fmt.Sprintf(
		"galley: task %s failed: supervisor_idle_timeout (supervisor=%s idle_timeout=%s tries=%d/%d; requeue or adjust daemon settings)",
		taskID, e.supervisorName(), e.IdleTimeout, e.Tries, e.MaxTries,
	)
}

// isIdleTimeoutError reports whether err originates from the idle-output
// watchdog.
func isIdleTimeoutError(err error) bool {
	return errors.Is(err, runner.ErrIdleTimeout)
}

// asSupervisorIdleTimeout reports whether err is, or wraps, an exhausted
// supervisor idle timeout.
func asSupervisorIdleTimeout(err error) (*supervisorIdleTimeoutError, bool) {
	var idle *supervisorIdleTimeoutError
	if errors.As(err, &idle) {
		return idle, true
	}
	return nil, false
}
