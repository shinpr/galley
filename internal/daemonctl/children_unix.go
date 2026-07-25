//go:build darwin || linux

package daemonctl

import (
	"errors"
	"syscall"
)

// killProcessGroupByID sends SIGKILL to every process in the pgid group.
// ESRCH (empty group) is treated as a successful no-op so the cleanup loop
// can confirm the group is gone via the alive probe rather than the kill
// return value.
func killProcessGroupByID(pgid int) error {
	if pgid <= 0 {
		return nil
	}
	err := syscall.Kill(-pgid, syscall.SIGKILL)
	if err == nil {
		return nil
	}
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

// processGroupAlive reports whether any process in the pgid group is still
// alive. signal(0) against -pgid is the conventional liveness probe; ESRCH
// indicates the group is empty, EPERM indicates the process still exists but
// belongs to another UID.
func processGroupAlive(pgid int) (bool, error) {
	if pgid <= 0 {
		return false, nil
	}
	err := syscall.Kill(-pgid, syscall.Signal(0))
	if err == nil {
		return true, nil
	}
	if errors.Is(err, syscall.ESRCH) {
		return false, nil
	}
	if errors.Is(err, syscall.EPERM) {
		return true, nil
	}
	return false, err
}
