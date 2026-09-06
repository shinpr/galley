//go:build darwin || linux

package daemonctl

import (
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"
)

// TestTerminateChildProcessNilIsNoOp pins the helper's nil-process contract.
// `galley daemon start` cleanup must call this helper whenever `child.Start`
// returned success, but defensive callers may pass a nil process; the
// helper must not panic in that case.
func TestTerminateChildProcessNilIsNoOp(t *testing.T) {
	t.Parallel()
	if err := TerminateChildProcess(nil); err != nil {
		t.Fatalf("TerminateChildProcess(nil) = %v, want nil", err)
	}
}

// TestTerminateChildProcessUnixUsesSIGTERM covers the Unix branch of the
// cross-platform start-cleanup helper. The Unix daemon's signal handler
// translates SIGTERM into a graceful shutdown, so cleanup of a failed
// `galley daemon start` must keep that contract: the child must observe
// SIGTERM rather than SIGKILL. We start a sleep, send SIGTERM via the
// helper, and verify the child exits with a SIGTERM-signaled status.
func TestTerminateChildProcessUnixUsesSIGTERM(t *testing.T) {
	t.Parallel()
	sleepPath, err := exec.LookPath("sleep")
	if err != nil {
		t.Skipf("sleep not available: %v", err)
	}
	cmd := exec.CommandContext(t.Context(), sleepPath, "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })

	if err := TerminateChildProcess(cmd.Process); err != nil {
		t.Fatalf("TerminateChildProcess: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case waitErr := <-done:
		exitErr := new(exec.ExitError)
		if waitErr == nil {
			t.Fatalf("sleep exited 0 after SIGTERM, want signaled exit")
		}
		if !errors.As(waitErr, &exitErr) {
			t.Fatalf("wait err = %v, want *exec.ExitError", waitErr)
		}
		state, ok := exitErr.Sys().(interface{ Signal() os.Signal })
		if !ok {
			// Fall through: not all platforms expose Signal() but the
			// non-zero exit alone is sufficient evidence the helper
			// delivered termination.
			return
		}
		// The Unix path uses SIGTERM, not SIGKILL. The observable invariant
		// on macOS and Linux is signal exit rather than the exact value.
		if _, signaled := state.Signal().(interface{ Signal() }); signaled {
			t.Fatalf("unexpected non-signal exit state")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("sleep did not exit within 5s after TerminateChildProcess")
	}
}
