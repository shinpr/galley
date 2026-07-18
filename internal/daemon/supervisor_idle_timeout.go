package daemon

import (
	"errors"
	"fmt"
	"time"

	"github.com/shinpr/galley/internal/runner"
)

// supervisorIdleTimeoutKind is the distinct attempt-error kind recorded when a
// task fails because the built-in supervisor hit its idle timeout. It is
// separate from the generic "idle_timeout" kind used for executors so operators and
// `galley task show` can tell a supervisor watchdog failure apart from an
// executor idle timeout or a task total-timeout expiry.
const supervisorIdleTimeoutKind = "supervisor_idle_timeout"

// supervisorIdleTimeoutError reports that the built-in supervisor subprocess
// produced no stdout/stderr for the configured --idle-timeout and was killed
// by the idle-output watchdog. evaluateSupervisor returns it instead of a plain
// wrapped error so the failure-reporting paths
// (attempt error, daemon log line, `galley task show`) can describe the
// failure as a supervisor watchdog timeout rather than the task's
// execution_policy.timeout_ms expiring.
type supervisorIdleTimeoutError struct {
	// Supervisor is the built-in supervisor adapter name (codex or claude).
	Supervisor string
	// IdleTimeout is the resolved daemon --idle-timeout watchdog duration.
	IdleTimeout time.Duration
	// Err is the underlying idle-timeout error from the supervisor runner.
	Err error
}

func (e *supervisorIdleTimeoutError) Error() string {
	return fmt.Sprintf(
		"supervisor %s produced no output and was killed by the idle-timeout watchdog: %v",
		e.supervisorName(), e.Err,
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
// duration, explicitly distinguishes the failure from the
// task execution_policy.timeout_ms expiring, and states the next action.
func (e *supervisorIdleTimeoutError) attemptErrorMessage() string {
	return fmt.Sprintf(
		"supervisor idle timeout: the built-in supervisor subprocess produced no output for the idle-timeout watchdog and was killed "+
			"(supervisor=%s idle_timeout=%s). This is a supervisor watchdog failure, not the task execution_policy.timeout_ms expiring. "+
			"Requeue the task, or adjust the daemon --idle-timeout or --supervisor settings.",
		e.supervisorName(), e.IdleTimeout,
	)
}

// logLine builds the one-line daemon stderr report for a supervisor idle
// timeout. It keeps the existing `galley: task <id> ...` log tone so
// daemon logs stay scannable while the fuller explanation lives in the attempt
// error message and `galley task show`.
func (e *supervisorIdleTimeoutError) logLine(taskID string) string {
	return fmt.Sprintf(
		"galley: task %s failed: supervisor_idle_timeout (supervisor=%s idle_timeout=%s; requeue or adjust daemon settings)",
		taskID, e.supervisorName(), e.IdleTimeout,
	)
}

// isIdleTimeoutError reports whether err originates from the idle-output
// watchdog.
func isIdleTimeoutError(err error) bool {
	return errors.Is(err, runner.ErrIdleTimeout)
}

// asSupervisorIdleTimeout reports whether err is, or wraps, a supervisor idle
// timeout.
func asSupervisorIdleTimeout(err error) (*supervisorIdleTimeoutError, bool) {
	var idle *supervisorIdleTimeoutError
	if errors.As(err, &idle) {
		return idle, true
	}
	return nil, false
}
