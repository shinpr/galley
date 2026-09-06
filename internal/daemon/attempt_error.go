package daemon

import (
	"context"
	"errors"
	"time"

	"github.com/shinpr/galley/internal/proc"
	"github.com/shinpr/galley/internal/supervisor"
	"github.com/shinpr/galley/internal/task"
)

// attemptFailure is one failed attempt Galley records on the task.
type attemptFailure struct {
	Phase       string
	Kind        string
	Err         error
	ArtifactDir string
}

func appendFailureAttempt(loaded *task.Task, failure attemptFailure) {
	if loaded == nil {
		return
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	message := ""
	if failure.Err != nil {
		message = failure.Err.Error()
	}
	loaded.Attempts = append(loaded.Attempts, task.Attempt{
		Number:            len(loaded.Attempts) + 1,
		StartedAt:         now,
		CompletedAt:       now,
		ClaudeStatus:      "not_run",
		SupervisorVerdict: failure.Kind,
		Summary:           message,
		Error:             attemptError(failure),
	})
}

func supervisorFailureKind(err error) string {
	if supervisor.IsVerdictContractError(err) {
		return "supervisor_invalid_verdict"
	}
	return classifyFailureKind("supervisor_failed", err)
}

func attemptError(failure attemptFailure) *task.AttemptError {
	message := ""
	if failure.Err != nil {
		message = failure.Err.Error()
	}
	return &task.AttemptError{
		Phase:       failure.Phase,
		Kind:        failure.Kind,
		Message:     message,
		ArtifactDir: failure.ArtifactDir,
	}
}

func classifyFailureKind(defaultKind string, err error) string {
	if err == nil {
		return defaultKind
	}
	// Classify via the runner's typed sentinels rather than error-message
	// substrings so a wording change in the runner cannot silently reclassify
	// an idle/total timeout as a generic executor failure and hide the
	// actionable timeout signal from downstream recovery.
	if errors.Is(err, proc.ErrIdleTimeout) {
		return "idle_timeout"
	}
	if errors.Is(err, proc.ErrTimeout) || errors.Is(err, context.DeadlineExceeded) {
		return "timed_out"
	}
	return defaultKind
}
