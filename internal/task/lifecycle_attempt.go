package task

import (
	"time"

	"github.com/shinpr/galley/internal/strutil"
)

// lifecycleAttempt is one non-executor attempt recorded on the task.
type lifecycleAttempt struct {
	Verdict  string
	Reason   string
	Fallback string
}

func appendLifecycleAttempt(t *Task, entry lifecycleAttempt, now time.Time) {
	verdict, reason, fallback := entry.Verdict, entry.Reason, entry.Fallback
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
