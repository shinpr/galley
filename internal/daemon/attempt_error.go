package daemon

import (
	"context"
	"errors"
	"strings"
	"time"

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
	msg := err.Error()
	if strings.Contains(msg, "idle timeout") {
		return "idle_timeout"
	}
	if errors.Is(err, context.DeadlineExceeded) || strings.Contains(msg, "timed out") {
		return "timed_out"
	}
	return defaultKind
}
