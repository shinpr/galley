package daemoncmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/shinpr/galley/internal/daemonctl"
)

type stopLockOwner struct {
	PID              int    `json:"pid"`
	ProcessStartedAt string `json:"process_started_at,omitempty"`
	Claim            string `json:"claim"`
}

func stopLockPath(pidFile string) string       { return pidFile + ".stop.lock" }
func stopLockOwnerPath(lockPath string) string { return filepath.Join(lockPath, "owner.json") }

// acquireStopLock serializes cooperating stop commands. A dead owner's lock is
// reclaimed; a live owner is never displaced by a time-based lease.
func acquireStopLock(pidFile string, timeout time.Duration) (release func(), err error) {
	path := stopLockPath(pidFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	owner := currentStopLockOwner()
	deadline := time.Now().Add(timeout)
	for {
		openErr := os.Mkdir(path, 0o700)
		if openErr == nil {
			data, marshalErr := json.Marshal(owner)
			if marshalErr == nil {
				marshalErr = os.WriteFile(stopLockOwnerPath(path), append(data, '\n'), 0o600)
			}
			if marshalErr != nil {
				_ = os.RemoveAll(path)
				return nil, marshalErr
			}
			return func() { releaseStopLock(path, owner) }, nil
		}
		if !errors.Is(openErr, os.ErrExist) {
			return nil, openErr
		}
		stale, staleErr := stopLockIsStale(path)
		if staleErr != nil {
			return nil, staleErr
		}
		if stale {
			tomb := fmt.Sprintf("%s.stale-%d-%d", path, os.Getpid(), time.Now().UnixNano())
			if renameErr := os.Rename(path, tomb); renameErr == nil {
				_ = os.RemoveAll(tomb)
			}
			continue
		}
		if timeout <= 0 || !time.Now().Before(deadline) {
			return nil, errors.New("timed out waiting for active daemon stop")
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func currentStopLockOwner() stopLockOwner {
	owner := stopLockOwner{PID: os.Getpid(), Claim: fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())}
	if info, err := daemonctl.ProcessInfo(owner.PID); err == nil {
		owner.ProcessStartedAt = info.StartedAt
	}
	return owner
}

func stopLockIsStale(path string) (bool, error) {
	data, err := os.ReadFile(stopLockOwnerPath(path))
	if err != nil {
		info, statErr := os.Stat(path)
		if errors.Is(statErr, os.ErrNotExist) {
			return true, nil
		}
		if statErr != nil {
			return false, statErr
		}
		return time.Since(info.ModTime()) >= time.Second, nil
	}
	var owner stopLockOwner
	if err := json.Unmarshal(data, &owner); err != nil || owner.PID <= 0 || owner.Claim == "" {
		info, statErr := os.Stat(path)
		if statErr != nil {
			return false, statErr
		}
		return time.Since(info.ModTime()) >= time.Second, nil
	}
	alive, err := daemonctl.Alive(owner.PID)
	if err != nil {
		return false, err
	}
	if !alive {
		return true, nil
	}
	if owner.ProcessStartedAt == "" {
		return false, nil
	}
	info, err := daemonctl.ProcessInfo(owner.PID)
	if err != nil {
		return false, nil
	}
	return info.StartedAt != "" && info.StartedAt != owner.ProcessStartedAt, nil
}

func releaseStopLock(path string, expected stopLockOwner) {
	data, err := os.ReadFile(stopLockOwnerPath(path))
	if err != nil {
		return
	}
	var current stopLockOwner
	if json.Unmarshal(data, &current) == nil && current.Claim == expected.Claim {
		_ = os.Remove(stopLockOwnerPath(path))
		_ = os.Remove(path)
	}
}

func sameDaemonIdentity(a, b daemonctl.PIDFile) bool {
	return a.PID == b.PID && a.ProcessStartedAt == b.ProcessStartedAt && a.TokenHash == b.TokenHash && a.Executable == b.Executable && a.Root == b.Root
}
