//go:build windows

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
	// TODO: Use a Windows Job Object so descendant processes are cleaned up
	// with the same strength as Unix process group termination.
	_ = cmd.Process.Kill()
}

// processGroupID falls back to the PID on Windows; force-stop child cleanup
// targets the same handle that killProcessGroup terminates above.
func processGroupID(cmd *exec.Cmd) (int, error) {
	if cmd.Process == nil {
		return 0, syscall.ESRCH
	}
	return cmd.Process.Pid, nil
}
