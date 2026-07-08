//go:build windows

package daemonctl

import (
	"os"
	"testing"
	"time"
)

// TestProcessInfoReturnsStableIdentityForSelf guarantees the contract Verify and
// interrupted-task recovery rely on: a live process yields a non-empty identity
// and a StartedAt that is stable across calls (so ProcessStartedAt comparisons
// do not spuriously flag a PID recycle).
func TestProcessInfoReturnsStableIdentityForSelf(t *testing.T) {
	first, err := ProcessInfo(os.Getpid())
	if err != nil {
		t.Fatalf("ProcessInfo for self: %v", err)
	}
	if first.StartedAt == "" {
		t.Fatal("StartedAt must be populated for a live process")
	}
	if _, err := time.Parse(time.RFC3339Nano, first.StartedAt); err != nil {
		t.Fatalf("StartedAt %q is not a parseable timestamp: %v", first.StartedAt, err)
	}
	if first.Executable == "" && first.Command == "" {
		t.Fatal("at least one of Executable/Command must identify the process")
	}
	second, err := ProcessInfo(os.Getpid())
	if err != nil {
		t.Fatalf("ProcessInfo (second call): %v", err)
	}
	if first.StartedAt != second.StartedAt {
		t.Fatalf("StartedAt not stable across calls: %q vs %q", first.StartedAt, second.StartedAt)
	}
}

// TestProcessInfoErrorsForMissingPid guarantees a dead/absent PID is reported as
// an error so callers treat it as not-live rather than as a verified match.
func TestProcessInfoErrorsForMissingPid(t *testing.T) {
	if _, err := ProcessInfo(0x7fffffff); err == nil {
		t.Fatal("expected an error for a non-existent PID")
	}
}
