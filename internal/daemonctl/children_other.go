//go:build !darwin && !linux && !freebsd && !netbsd && !openbsd

package daemonctl

import (
	"errors"
	"os"
	"syscall"
)

// killProcessGroupByID and processGroupAlive degrade to PID-level operations
// on platforms without Unix process groups. The runner package only honors
// Setpgid on Unix; on other platforms Galley does not actually create child
// process groups, so PID-level reasoning is the most faithful fallback.
func killProcessGroupByID(pgid int) error {
	if pgid <= 0 {
		return nil
	}
	process, err := os.FindProcess(pgid)
	if err != nil {
		return err
	}
	if err := process.Kill(); err != nil {
		if errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}
	return nil
}

func processGroupAlive(pgid int) (bool, error) {
	if pgid <= 0 {
		return false, nil
	}
	return Alive(pgid)
}
