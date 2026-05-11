//go:build !darwin && !linux && !freebsd && !netbsd && !openbsd && !windows

package runner

import (
	"os/exec"
	"syscall"
)

func processGroupAttr() *syscall.SysProcAttr {
	return nil
}

func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
}

// processGroupID falls back to the process PID on platforms without
// Unix-style process groups. Child-process force-stop cleanup uses the result
// as a best-effort identifier.
func processGroupID(cmd *exec.Cmd) (int, error) {
	if cmd.Process == nil {
		return 0, syscall.ESRCH
	}
	return cmd.Process.Pid, nil
}
