//go:build windows

package daemonctl

import (
	"errors"
	"fmt"
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
		return fmt.Errorf("find process %d: %w", pgid, err)
	}
	if err := process.Kill(); err != nil {
		if errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return fmt.Errorf("terminate process %d: %w", pgid, err)
	}
	return nil
}

func processGroupAlive(pgid int) (bool, error) {
	if pgid <= 0 {
		return false, nil
	}
	return Alive(pgid)
}
