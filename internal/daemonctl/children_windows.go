//go:build windows

package daemonctl

import (
	"errors"
	"os"
	"syscall"
)

// Windows tracks the child PID because it has no Unix process groups.
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
