//go:build darwin || linux

package proc

import (
	"fmt"
	"os/exec"
	"syscall"
)

func processGroupAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}

func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		_ = cmd.Process.Kill()
		return
	}
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
}

// processGroupID returns the OS process group id for cmd's spawned child.
// Used by the child registry so daemon stop --force can SIGKILL the same
// process group that killProcessGroup targets.
func processGroupID(cmd *exec.Cmd) (int, error) {
	if cmd.Process == nil {
		return 0, syscall.ESRCH
	}
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		return 0, fmt.Errorf("get process group for pid %d: %w", cmd.Process.Pid, err)
	}
	return pgid, nil
}
