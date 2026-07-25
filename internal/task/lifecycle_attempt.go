package task

import (
	"time"

	"github.com/shinpr/galley/internal/strutil"
)

func appendLifecycleAttempt(t *Task, verdict, reason, fallback string, now time.Time) {
	timestamp := now.UTC().Format(time.RFC3339Nano)
	t.Attempts = append(t.Attempts, Attempt{
		Number:            len(t.Attempts) + 1,
		StartedAt:         timestamp,
		CompletedAt:       timestamp,
		ClaudeStatus:      "not_run",
		SupervisorVerdict: verdict,
		Summary:           strutil.FirstNonEmpty(reason, fallback),
	})
}
