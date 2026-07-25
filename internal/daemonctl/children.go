package daemonctl

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/shinpr/galley/internal/proc"
)

// ErrChildCleanupIncomplete signals that force stop could not confirm every
// known child process group exited. The error message names the surviving
// process identifiers so the operator can act on them instead of receiving a
// silent "clean stop" report.
type ErrChildCleanupIncomplete struct {
	Remaining []proc.ChildRecord
}

func (e *ErrChildCleanupIncomplete) Error() string {
	if e == nil || len(e.Remaining) == 0 {
		return "child process group cleanup incomplete"
	}
	parts := make([]string, 0, len(e.Remaining))
	for _, rec := range e.Remaining {
		parts = append(parts, fmt.Sprintf("pid=%d pgid=%d argv0=%s", rec.PID, rec.PGID, rec.Argv0))
	}
	return fmt.Sprintf("child process group cleanup incomplete: %s", strings.Join(parts, "; "))
}

// childKiller decouples the SIGKILL and alive probes from the cleanup
// orchestration so tests can simulate an unkillable child without spawning
// one (which would be impossible on Unix since SIGKILL cannot be trapped).
type childKiller struct {
	KillPGID  func(pgid int) error
	AlivePID  func(pid int) (bool, error)
	AlivePGID func(pgid int) (bool, error)
}

func defaultChildKiller() childKiller {
	return childKiller{
		KillPGID:  killProcessGroupByID,
		AlivePID:  Alive,
		AlivePGID: processGroupAlive,
	}
}

// CleanupRegisteredChildren reads the Galley child process registry under
// registryPath, SIGKILLs every still-alive registered process group, waits
// up to timeout for them to exit, and removes the registry file when every
// group is gone. It returns ErrChildCleanupIncomplete (with the surviving
// records) if any process group is still alive after the wait, so the stop
// command can report concrete PIDs rather than silently succeeding.
//
// An empty registryPath, a missing registry file, or an empty registry are
// no-ops that return nil.
func CleanupRegisteredChildren(registryPath string, timeout time.Duration) ([]proc.ChildRecord, error) {
	return cleanupRegisteredChildren(registryPath, timeout, defaultChildKiller())
}

func cleanupRegisteredChildren(registryPath string, timeout time.Duration, killer childKiller) ([]proc.ChildRecord, error) {
	if registryPath == "" {
		return nil, nil
	}
	reg := proc.NewChildRegistry(registryPath)
	records, err := reg.List(func(rec proc.ChildRecord) (bool, error) {
		// Probe the process group, not just the leader PID. A force-killed or
		// already-reaped leader can leave surviving descendants in the same
		// pgid; pruning the record on a dead leader PID alone would orphan
		// them. When the record carries a pgid the group probe is therefore
		// authoritative: return its result directly so the record is kept on a
		// probe error or while the group is alive, and pruned only when the
		// group is confirmed gone. Fall back to the PID probe only when the
		// record carries no pgid (non-Unix platforms that do not create child
		// process groups).
		if rec.PGID > 0 {
			return killer.AlivePGID(rec.PGID)
		}
		return killer.AlivePID(rec.PID)
	})
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		_ = reg.Clear()
		return nil, nil
	}
	// Snapshot before mutating so the returned record list is stable. Sort
	// by PID for deterministic output in error messages and tests.
	snapshot := make([]proc.ChildRecord, len(records))
	copy(snapshot, records)
	sort.Slice(snapshot, func(i, j int) bool { return snapshot[i].PID < snapshot[j].PID })

	for _, rec := range snapshot {
		if rec.PGID <= 0 {
			continue
		}
		if err := killer.KillPGID(rec.PGID); err != nil {
			// Record the kill error per child but keep going so a single
			// unkillable group does not abort cleanup of the others.
			fmt.Fprintf(os.Stderr, "galley: SIGKILL pgid %d failed: %v\n", rec.PGID, err)
		}
	}
	deadline := time.Now().Add(timeout)
	remaining := snapshot
	for {
		stillAlive := remaining[:0]
		for _, rec := range remaining {
			alive, aerr := killer.AlivePGID(rec.PGID)
			if aerr != nil || alive {
				stillAlive = append(stillAlive, rec)
			}
		}
		if len(stillAlive) == 0 {
			_ = reg.Clear()
			return nil, nil
		}
		if timeout <= 0 || time.Now().After(deadline) {
			killed := make([]proc.ChildRecord, 0, len(snapshot))
			survivors := stillAlive
			for _, rec := range snapshot {
				dead := true
				for _, alive := range survivors {
					if alive.PID == rec.PID {
						dead = false
						break
					}
				}
				if dead {
					killed = append(killed, rec)
				}
			}
			_ = pruneKilledFromRegistry(reg, killed)
			return survivors, &ErrChildCleanupIncomplete{Remaining: survivors}
		}
		time.Sleep(50 * time.Millisecond)
		remaining = stillAlive
	}
}

func pruneKilledFromRegistry(reg *proc.ChildRegistry, killed []proc.ChildRecord) error {
	for _, rec := range killed {
		if err := reg.Unregister(rec.PID); err != nil {
			return err
		}
	}
	return nil
}
