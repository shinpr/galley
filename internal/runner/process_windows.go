//go:build windows

package runner

import (
	"os/exec"
	"strconv"
	"syscall"
)

func processGroupAttr() *syscall.SysProcAttr {
	return nil
}

func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	// taskkill /T terminates the process together with the descendants it
	// spawned (node, test runners, git), matching Unix process-group termination
	// so an idle/timeout/cancel does not leave orphaned children holding worktree
	// locks. If taskkill cannot terminate the tree, fall back to killing the
	// direct child so the call never becomes a no-op.
	if err := exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(cmd.Process.Pid)).Run(); err != nil {
		_ = cmd.Process.Kill()
	}
}

// processGroupID falls back to the PID on Windows; force-stop child cleanup
// targets the same handle that killProcessGroup terminates above.
func processGroupID(cmd *exec.Cmd) (int, error) {
	if cmd.Process == nil {
		return 0, syscall.ESRCH
	}
	return cmd.Process.Pid, nil
}
