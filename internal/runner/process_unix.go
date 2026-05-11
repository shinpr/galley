//go:build darwin || linux || freebsd || netbsd || openbsd

package runner

import (
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
	return syscall.Getpgid(cmd.Process.Pid)
}
