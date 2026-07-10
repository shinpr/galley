//go:build windows

package daemonctl

import (
	"os/exec"
	"testing"
	"time"
)

// TestTerminateChildProcessWindowsNilIsNoOp pins the Windows nil-process
// contract. `galley daemon start` cleanup must call this helper whenever
// `child.Start` returned success, but defensive callers may pass nil; the
// helper must not panic.
func TestTerminateChildProcessWindowsNilIsNoOp(t *testing.T) {
	t.Parallel()
	if err := TerminateChildProcess(nil); err != nil {
		t.Fatalf("TerminateChildProcess(nil) = %v, want nil", err)
	}
}

// TestTerminateChildProcessWindowsTerminatesLivingProcess covers the
// Windows branch of the cross-platform start-cleanup helper. Windows has no
// SIGTERM equivalent (`process.Signal(syscall.SIGTERM)` returns
// "not supported by windows" and leaves the child running), so the helper
// must invoke `Process.Kill` (TerminateProcess) to ensure the child is gone
// before a failed `galley daemon start` returns an error.
func TestTerminateChildProcessWindowsTerminatesLivingProcess(t *testing.T) {
	t.Parallel()
	cmd := exec.Command("cmd.exe", "/C", "ping -n 30 127.0.0.1 > NUL")
	if err := cmd.Start(); err != nil {
		t.Skipf("cmd.exe not available: %v", err)
	}
	pid := cmd.Process.Pid
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })

	if err := TerminateChildProcess(cmd.Process); err != nil {
		t.Fatalf("TerminateChildProcess: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		alive, err := Alive(pid)
		if err != nil {
			t.Fatalf("Alive after TerminateChildProcess: %v", err)
		}
		if !alive {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("pid %d still alive after TerminateChildProcess on Windows", pid)
}
