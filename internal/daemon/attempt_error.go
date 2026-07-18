package daemon

import (
	"context"
	"errors"
	"time"

	"github.com/shinpr/galley/internal/runner"
	"github.com/shinpr/galley/internal/supervisor"
	"github.com/shinpr/galley/internal/task"
)

func appendFailureAttempt(loaded *task.Task, phase, kind string, err error, artifactDir string) {
	if loaded == nil {
		return
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	message := ""
	if err != nil {
		message = err.Error()
	}
	loaded.Attempts = append(loaded.Attempts, task.Attempt{
		Number:            len(loaded.Attempts) + 1,
		StartedAt:         now,
		CompletedAt:       now,
		ClaudeStatus:      "not_run",
		SupervisorVerdict: kind,
		Summary:           message,
		Error:             attemptError(phase, kind, err, artifactDir),
	})
}

func supervisorFailureKind(err error) string {
	if supervisor.IsVerdictContractError(err) {
		return "supervisor_invalid_verdict"
	}
	return classifyFailureKind("supervisor_failed", err)
}

func attemptError(phase, kind string, err error, artifactDir string) *task.AttemptError {
	message := ""
	if err != nil {
		message = err.Error()
	}
	return &task.AttemptError{
		Phase:       phase,
		Kind:        kind,
		Message:     message,
		ArtifactDir: artifactDir,
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
	if errors.Is(err, runner.ErrIdleTimeout) {
		return "idle_timeout"
	}
	if errors.Is(err, runner.ErrTimeout) || errors.Is(err, context.DeadlineExceeded) {
		return "timed_out"
	}
	return defaultKind
}
