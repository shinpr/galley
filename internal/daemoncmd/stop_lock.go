package daemoncmd

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/shinpr/galley/internal/daemonctl"
)

func stopIntentPath(pidFile string, target daemonctl.PIDFile) string {
	identity, _ := json.Marshal(struct {
		PID              int    `json:"pid"`
		ProcessStartedAt string `json:"process_started_at"`
		StartedAt        string `json:"started_at"`
		TokenHash        string `json:"token_hash"`
	}{target.PID, target.ProcessStartedAt, target.StartedAt, target.TokenHash})
	sum := sha256.Sum256(identity)
	return pidFile + ".stop-" + hex.EncodeToString(sum[:8])
}

// claimStopIntent makes a stop signal single-owner for one daemon identity.
// The intent remains after a timeout so a later normal stop cannot re-signal.
func claimStopIntent(pidFile string, target daemonctl.PIDFile) (path string, leader bool, err error) {
	path = stopIntentPath(pidFile, target)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", false, err
	}
	err = os.Mkdir(path, 0o700)
	if err == nil {
		return path, true, nil
	}
	if errors.Is(err, os.ErrExist) {
		return path, false, nil
	}
	return "", false, err
}

func waitForDaemonStop(pidFile, root, executable string, target daemonctl.PIDFile, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		status, err := daemonctl.Inspect(pidFile, root, executable)
		if errors.Is(err, daemonctl.ErrNotRunning) {
			return nil
		}
		if err != nil {
			return err
		}
		if !sameDaemonIdentity(target, status.Meta) || !status.Alive {
			return nil
		}
		if timeout <= 0 || !time.Now().Before(deadline) {
			return fmt.Errorf("daemon pid %d did not stop within %s; shutdown remains in progress, so normal stop will not signal again; use stop --force to recover, which interrupts active attempts", target.PID, timeout)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func removeStopIntent(path string) {
	_ = os.Remove(path)
}

func sameDaemonIdentity(a, b daemonctl.PIDFile) bool {
	return a.PID == b.PID && a.ProcessStartedAt == b.ProcessStartedAt && a.StartedAt == b.StartedAt && a.TokenHash == b.TokenHash && a.Executable == b.Executable && a.Root == b.Root
}
