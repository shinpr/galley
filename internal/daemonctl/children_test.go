package daemonctl

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shinpr/galley/internal/proc"
)

// TestCleanupRegisteredChildrenSurfacesSurvivingPIDs exercises AC-005: if a
// registered child cannot be killed during force stop, cleanup must return a
// user-visible error that names the surviving PID/PGID rather than silently
// reporting a clean stop. Real SIGKILL cannot be trapped on Unix, so the test
// injects a childKiller that pretends the process is unkillable and verifies
// the resulting ErrChildCleanupIncomplete carries the registered record. This
// test is platform-agnostic because it never spawns a real subprocess.
func TestCleanupRegisteredChildrenSurfacesSurvivingPIDs(t *testing.T) {
	t.Parallel()
	registryPath := filepath.Join(t.TempDir(), "children.json")
	reg := proc.NewChildRegistry(registryPath)
	rec := proc.ChildRecord{PID: 424242, PGID: 424242, Argv0: "fake-supervisor"}
	if err := reg.Register(rec); err != nil {
		t.Fatalf("register fake child: %v", err)
	}

	var killCalls int32
	killer := childKiller{
		KillPGID: func(pgid int) error {
			atomic.AddInt32(&killCalls, 1)
			return nil
		},
		AlivePID:  func(pid int) (bool, error) { return true, nil },
		AlivePGID: func(pgid int) (bool, error) { return true, nil },
	}

	survivors, err := cleanupRegisteredChildren(registryPath, 100*time.Millisecond, killer)
	if err == nil {
		t.Fatal("expected ErrChildCleanupIncomplete, got nil")
	}
	var incomplete *ErrChildCleanupIncomplete
	if !errors.As(err, &incomplete) {
		t.Fatalf("error type got %T, want *ErrChildCleanupIncomplete", err)
	}
	if len(survivors) != 1 || survivors[0].PID != rec.PID {
		t.Fatalf("survivors got %#v, want one record for pid %d", survivors, rec.PID)
	}
	if atomic.LoadInt32(&killCalls) == 0 {
		t.Fatal("KillPGID was never invoked")
	}
	// The user-visible error message must name the surviving PID and PGID so an
	// operator can target them instead of receiving a falsely-clean stop report.
	msg := err.Error()
	wantPID := fmt.Sprintf("pid=%d", rec.PID)
	wantPGID := fmt.Sprintf("pgid=%d", rec.PGID)
	if !strings.Contains(msg, wantPID) || !strings.Contains(msg, wantPGID) {
		t.Fatalf("error message %q must contain %q and %q", msg, wantPID, wantPGID)
	}
}

// TestCleanupRegisteredChildrenPrunesRecordWhenProcessGroupDead exercises the
// pgid-authoritative liveness rule: when a record carries a pgid, the process
// group probe alone decides whether the record stays. A dead process group must
// prune the record (and clear the now-empty registry) even if the leader PID
// still looks alive — a reused PID outside the group is not our child. Cleanup
// must then take no kill or survivor path. The probes are injected so the test
// never spawns a real subprocess and stays platform-agnostic.
func TestCleanupRegisteredChildrenPrunesRecordWhenProcessGroupDead(t *testing.T) {
	t.Parallel()
	registryPath := filepath.Join(t.TempDir(), "children.json")
	reg := proc.NewChildRegistry(registryPath)
	rec := proc.ChildRecord{PID: 525252, PGID: 525252, Argv0: "fake-supervisor"}
	if err := reg.Register(rec); err != nil {
		t.Fatalf("register fake child: %v", err)
	}

	var killCalls, alivePIDCalls int32
	killer := childKiller{
		KillPGID: func(pgid int) error {
			atomic.AddInt32(&killCalls, 1)
			return nil
		},
		AlivePID: func(pid int) (bool, error) {
			atomic.AddInt32(&alivePIDCalls, 1)
			return true, nil
		},
		AlivePGID: func(pgid int) (bool, error) { return false, nil },
	}

	survivors, err := cleanupRegisteredChildren(registryPath, 100*time.Millisecond, killer)
	if err != nil {
		t.Fatalf("cleanup returned error: %v", err)
	}
	if survivors != nil {
		t.Fatalf("survivors got %#v, want nil", survivors)
	}
	if atomic.LoadInt32(&killCalls) != 0 {
		t.Fatalf("KillPGID calls got %d, want 0", atomic.LoadInt32(&killCalls))
	}
	if atomic.LoadInt32(&alivePIDCalls) != 0 {
		t.Fatalf("AlivePID calls got %d, want 0 (pgid probe is authoritative)", atomic.LoadInt32(&alivePIDCalls))
	}
	// The dead-group record must be pruned, leaving the registry empty.
	remaining, err := reg.List(nil)
	if err != nil {
		t.Fatalf("list registry: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("registry records got %#v, want empty after pruning", remaining)
	}
}
