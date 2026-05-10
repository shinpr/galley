package result

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestCheckpointResultsCapturesPassingExitCodeAndOutput verifies the
// passing case: zero exit code, source tagging, and stdout excerpt all reach
// the CheckpointResult so supervisor evidence and task show can render the
// success without re-running the command.
func TestCheckpointResultsCapturesPassingExitCodeAndOutput(t *testing.T) {
	t.Parallel()
	results := RunSkeletonCheckpoints(context.Background(), t.TempDir(), []CheckpointSpec{{
		ACID:    "AC1",
		Command: "echo skeleton-pass-marker",
	}}, time.Second*5)
	if len(results) != 1 {
		t.Fatalf("expected one result, got %d", len(results))
	}
	got := results[0]
	if got.Status != "passed" {
		t.Fatalf("status = %q, want passed", got.Status)
	}
	if got.ExitCode != 0 {
		t.Fatalf("exit_code = %d, want 0", got.ExitCode)
	}
	if got.Source != CheckpointSourceAcceptanceSkeleton {
		t.Fatalf("source = %q, want %q", got.Source, CheckpointSourceAcceptanceSkeleton)
	}
	if !strings.Contains(got.StdoutExcerpt, "skeleton-pass-marker") {
		t.Fatalf("stdout_excerpt missing marker: %q", got.StdoutExcerpt)
	}
}

// TestCheckpointResultsRecordsFailureAndStderr verifies that a non-zero
// shell exit becomes a failed CheckpointResult while still preserving exit
// code, source tag, and a bounded stderr excerpt for evidence.
func TestCheckpointResultsRecordsFailureAndStderr(t *testing.T) {
	t.Parallel()
	results := RunSkeletonCheckpoints(context.Background(), t.TempDir(), []CheckpointSpec{{
		ACID:    "AC2",
		Command: "echo skeleton-fail-stderr 1>&2; exit 7",
	}}, time.Second*5)
	if len(results) != 1 {
		t.Fatalf("expected one result, got %d", len(results))
	}
	got := results[0]
	if got.Status != "failed" {
		t.Fatalf("status = %q, want failed", got.Status)
	}
	if got.ExitCode != 7 {
		t.Fatalf("exit_code = %d, want 7", got.ExitCode)
	}
	if got.Source != CheckpointSourceAcceptanceSkeleton {
		t.Fatalf("source = %q, want %q", got.Source, CheckpointSourceAcceptanceSkeleton)
	}
	if !strings.Contains(got.StderrExcerpt, "skeleton-fail-stderr") {
		t.Fatalf("stderr_excerpt missing marker: %q", got.StderrExcerpt)
	}
}

// TestCheckpointResultsRecordsDuration verifies the duration field is
// populated based on wall-clock time between command start and exit. Test uses
// a small but observable sleep so flakiness from coarse timers is avoided.
func TestCheckpointResultsRecordsDuration(t *testing.T) {
	t.Parallel()
	results := RunSkeletonCheckpoints(context.Background(), t.TempDir(), []CheckpointSpec{{
		ACID:    "AC3",
		Command: "sleep 0.05",
	}}, time.Second*5)
	if len(results) != 1 {
		t.Fatalf("expected one result, got %d", len(results))
	}
	if results[0].DurationMS < 30 {
		t.Fatalf("duration_ms = %d, want >= 30", results[0].DurationMS)
	}
}

// TestCheckpointResultsSkipsEmptyCommand verifies that an empty checkpoint
// command is recorded as "skipped" with the canonical source tag rather than
// invoked through the shell. Skipped checkpoints participate in the daemon
// acceptance gate so empty commands cannot silently pass.
func TestCheckpointResultsSkipsEmptyCommand(t *testing.T) {
	t.Parallel()
	results := RunSkeletonCheckpoints(context.Background(), t.TempDir(), []CheckpointSpec{{
		ACID:    "AC4",
		Command: "   ",
	}}, time.Second*5)
	if len(results) != 1 {
		t.Fatalf("expected one result, got %d", len(results))
	}
	got := results[0]
	if got.Status != "skipped" {
		t.Fatalf("status = %q, want skipped", got.Status)
	}
	if got.ExitCode != 0 {
		t.Fatalf("exit_code = %d, want 0", got.ExitCode)
	}
	if got.Source != CheckpointSourceAcceptanceSkeleton {
		t.Fatalf("source = %q, want %q", got.Source, CheckpointSourceAcceptanceSkeleton)
	}
}
