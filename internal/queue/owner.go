package queue

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const ownerSuffix = ".owner"

// Owner records which daemon process claimed a running task so startup recovery
// can distinguish a task interrupted by a dead daemon from one a live daemon
// still owns.
type Owner struct {
	PID              int    `json:"pid"`
	ProcessStartedAt string `json:"process_started_at,omitempty"`
	RecordedAt       string `json:"recorded_at"`
}

// OwnerPath returns the owner sidecar path for a running task file.
func OwnerPath(runningPath string) string {
	return runningPath + ownerSuffix
}

// WriteOwner records the owning daemon for a claimed running task.
func WriteOwner(runningPath string, owner Owner) error {
	data, err := json.Marshal(owner)
	if err != nil {
		return err
	}
	if err := os.WriteFile(OwnerPath(runningPath), data, 0o600); err != nil {
		return fmt.Errorf("write task owner %s: %w", OwnerPath(runningPath), err)
	}
	return nil
}

// ReadOwner reads the owning daemon for a running task. It returns os.ErrNotExist
// when no owner sidecar exists (for example, a task claimed by an older Galley).
func ReadOwner(runningPath string) (Owner, error) {
	data, err := os.ReadFile(OwnerPath(runningPath))
	if err != nil {
		return Owner{}, err
	}
	var owner Owner
	if err := json.Unmarshal(data, &owner); err != nil || owner.PID <= 0 {
		return Owner{}, fmt.Errorf("invalid task owner file %s", OwnerPath(runningPath))
	}
	return owner, nil
}

// RemoveOwner removes the owner sidecar for a running task if present.
func RemoveOwner(runningPath string) error {
	if err := os.Remove(OwnerPath(runningPath)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// RecoverInterruptedRunning requeues running tasks whose recorded owning daemon
// is dead or cannot be verified, without waiting for the claim TTL. Tasks whose
// ownerLive reports true are left untouched so a concurrently running daemon
// keeps its work.
//
// A running task with no owner sidecar, or with an unreadable/invalid one, is
// deliberately left alone here: it may be a task claimed by an older Galley, a
// claim whose owner sidecar has not been written yet, or live work owned by a
// concurrently running daemon that did not record ownership. None of those can
// be requeued safely just because the metadata is absent, so they fall through
// to the mtime-based ClaimTTL recovery (RecoverStaleClaims), which only acts
// once the claim is genuinely stale. Orphan owner sidecars (no matching task
// file) are removed.
func RecoverInterruptedRunning(root string, ownerLive func(Owner) (bool, error)) error {
	runningDir := filepath.Join(root, "tasks", "running")
	entries, err := os.ReadDir(runningDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read running dir %s: %w", runningDir, err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if !entry.Type().IsRegular() || !isTaskYAMLName(name) {
			continue
		}
		runningPath := filepath.Join(runningDir, name)
		owner, ownerErr := ReadOwner(runningPath)
		if ownerErr != nil {
			// No usable owner metadata: defer to the ClaimTTL backstop rather than
			// requeue, so freshly claimed live work is never stolen.
			continue
		}
		live, err := ownerLive(owner)
		if err != nil {
			return err
		}
		if live {
			continue
		}
		// The daemon that recorded ownership of this task is gone, so the task is
		// interrupted. Requeue it now rather than waiting for the claim TTL.
		if err := requeueRunningTask(root, runningPath); err != nil {
			if errors.Is(err, os.ErrExist) {
				continue
			}
			return err
		}
		_ = RemoveOwner(runningPath)
	}
	return removeOrphanOwners(runningDir)
}

func removeOrphanOwners(runningDir string) error {
	entries, err := os.ReadDir(runningDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read running dir %s: %w", runningDir, err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if !entry.Type().IsRegular() || filepath.Ext(name) != ownerSuffix {
			continue
		}
		taskName := name[:len(name)-len(ownerSuffix)]
		if _, err := os.Stat(filepath.Join(runningDir, taskName)); errors.Is(err, os.ErrNotExist) {
			if err := os.Remove(filepath.Join(runningDir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("remove orphan owner %s: %w", name, err)
			}
		}
	}
	return nil
}
