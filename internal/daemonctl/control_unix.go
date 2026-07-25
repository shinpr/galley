//go:build darwin || linux

package daemonctl

import (
	"errors"
	"os"
	"syscall"
	"time"
)

// Alive reports whether pid exists. Unix uses signal(0) as the conventional
// liveness probe.
func Alive(pid int) (bool, error) {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false, err
	}
	err = process.Signal(syscall.Signal(0))
	if err == nil {
		if Zombie(pid) {
			return false, nil
		}
		return true, nil
	}
	if errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH) {
		return false, nil
	}
	if errors.Is(err, syscall.EPERM) {
		return true, nil
	}
	return false, err
}

// Stop sends SIGTERM and waits for process exit until timeout.
func Stop(pid int, timeout time.Duration) error {
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	if err := process.Signal(syscall.SIGTERM); err != nil {
		if errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH) {
			return ErrNotRunning
		}
		return err
	}
	return waitExit(pid, timeout, "stop")
}
