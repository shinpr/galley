package task

import (
	"testing"
	"time"
)

func TestAppendLifecycleAttemptRecordsOneInstant(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 25, 10, 11, 12, 13, time.FixedZone("offset", 9*60*60))
	task := Task{Attempts: []Attempt{{Number: 1}}}
	appendLifecycleAttempt(&task, lifecycleAttempt{Verdict: "requeued", Reason: "", Fallback: "fallback"}, now)

	got := task.Attempts[1]
	if got.Number != 2 || got.StartedAt != got.CompletedAt {
		t.Fatalf("attempt numbering/time got %+v", got)
	}
	if got.StartedAt != "2026-07-25T01:11:12.000000013Z" {
		t.Fatalf("timestamp got %q", got.StartedAt)
	}
	if got.SupervisorVerdict != "requeued" || got.ClaudeStatus != "not_run" || got.Summary != "fallback" {
		t.Fatalf("attempt fields got %+v", got)
	}
}
