//go:build windows

package daemonctl

import (
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"
)

// TestAliveReturnsFalseForInvalidPIDs covers the AC4 Windows boundary for
// process verification. The Unix implementation conventionally short-circuits
// non-positive PIDs through `signal(0)` semantics; the Windows implementation
// must short-circuit explicitly because `OpenProcess(0)` and negative PID
// casts surface as raw Windows API errors rather than the expected
// "process not found" result. A reported-alive PID of 0 or below would also
// be a use-after-free hazard against a reused PID slot.
func TestAliveReturnsFalseForInvalidPIDs(t *testing.T) {
	t.Parallel()
	cases := []int{0, -1, -100}
	for _, pid := range cases {
		alive, err := Alive(pid)
		if err != nil {
			t.Fatalf("Alive(%d) returned error: %v", pid, err)
		}
		if alive {
			t.Fatalf("Alive(%d) = true, want false", pid)
		}
	}
}

// TestAliveReportsCurrentProcessAlive verifies that the Windows Alive path
// recognizes a known-live process through `OpenProcess` +
// `GetExitCodeProcess`. The current Go test process is guaranteed alive for
// the duration of the test and is the cheapest live-process fixture
// available on Windows runners.
func TestAliveReportsCurrentProcessAlive(t *testing.T) {
	t.Parallel()
	alive, err := Alive(os.Getpid())
	if err != nil {
		t.Fatalf("Alive(self) returned error: %v", err)
	}
	if !alive {
		t.Fatal("Alive(self) = false, want true")
	}
}

// TestAliveReportsExitedProcessNotAlive covers the Windows-specific
// boundary that `OpenProcess` may still succeed for a recently-exited
// process while `GetExitCodeProcess` returns a non-STILL_ACTIVE code. The
// Unix path uses signal(0)/ESRCH; the Windows path relies on the exit-code
// comparison and must report not-alive once a child has exited.
func TestAliveReportsExitedProcessNotAlive(t *testing.T) {
	t.Parallel()
	cmd := exec.Command("cmd.exe", "/C", "exit 0")
	if err := cmd.Start(); err != nil {
		t.Skipf("cmd.exe not available: %v", err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}
	// Give the kernel a brief window to publish the exit code; the loop is
	// bounded so a hung test cannot deadlock CI.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		alive, err := Alive(pid)
		if err != nil {
			t.Fatalf("Alive(exited) error: %v", err)
		}
		if !alive {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("Alive(exited pid %d) still reports alive after 2s", pid)
}

// TestStopReturnsNotRunningForExitedPID covers the AC4 Windows boundary for
// termination of an already-stopped process. Windows `process.Kill` against
// an exited process returns `os.ErrProcessDone`, which the build-tagged Stop
// must translate to ErrNotRunning so daemon stop reports a clean stopped
// state instead of surfacing a raw "process already finished" error.
func TestStopReturnsNotRunningForExitedPID(t *testing.T) {
	t.Parallel()
	cmd := exec.Command("cmd.exe", "/C", "exit 0")
	if err := cmd.Start(); err != nil {
		t.Skipf("cmd.exe not available: %v", err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if err := Stop(pid, 500*time.Millisecond); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("Stop(exited pid) = %v, want ErrNotRunning", err)
	}
}

// TestStopTerminatesLivingProcess covers the AC4 Windows boundary that
// daemon stop must actually kill a live background process even though
// Windows has no SIGTERM equivalent for a console-less daemon. The
// background-daemon limitation documented in CHANGELOG.md and
// docs/operations.md is "no graceful shutdown" — the immediate
// TerminateProcess path is still required to leave the process gone.
func TestStopTerminatesLivingProcess(t *testing.T) {
	t.Parallel()
	cmd := exec.Command("cmd.exe", "/C", "ping -n 30 127.0.0.1 > NUL")
	if err := cmd.Start(); err != nil {
		t.Skipf("cmd.exe not available: %v", err)
	}
	pid := cmd.Process.Pid
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })

	if err := Stop(pid, 2*time.Second); err != nil {
		t.Fatalf("Stop(live pid): %v", err)
	}
	alive, err := Alive(pid)
	if err != nil {
		t.Fatalf("Alive after Stop: %v", err)
	}
	if alive {
		t.Fatal("process still alive after Stop on Windows")
	}
}

// TestForceStopOnLiveProcessKills covers the AC4 Windows boundary that
// `ForceStop` escalates correctly when the graceful stop path is in fact
// immediate-termination. The PID metadata must verify against the running
// process; the verification helper short-circuits on platforms that lack
// process-table introspection, so the test skips when verification is not
// available in the runner environment (mirroring the Unix force tests).
func TestForceStopOnLiveProcessKills(t *testing.T) {
	t.Parallel()
	cmdPath, err := exec.LookPath("cmd.exe")
	if err != nil {
		t.Skipf("cmd.exe not available: %v", err)
	}
	cmd := exec.Command(cmdPath, "/C", "ping -n 30 127.0.0.1 > NUL")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
	meta := NewPIDFile(cmd.Process.Pid, cmdPath, t.TempDir(), []string{cmdPath})
	if !Verify(meta, meta.Root, meta.Executable) {
		t.Skip("process identity verification unavailable on this Windows runner")
	}
	if _, err := ForceStop(meta, 2*time.Second); err != nil {
		t.Fatalf("ForceStop: %v", err)
	}
	alive, err := Alive(cmd.Process.Pid)
	if err != nil {
		t.Fatalf("Alive after ForceStop: %v", err)
	}
	if alive {
		t.Fatal("process still alive after ForceStop on Windows")
	}
}

// TestChildProcessGroupCleanupFallsBackToPID documents the AC4 Windows
// limitation around child cleanup: Galley does not create Unix process
// groups on Windows (the runner package only sets `Setpgid` on Unix), so
// `killProcessGroupByID` degrades to the same primitive as `process.Kill`
// — terminating a single PID rather than every descendant. The test
// pins this contract so future refactors of the children_other.go fallback
// continue to leave a verified, terminated leader rather than crashing
// or skipping cleanup entirely.
func TestChildProcessGroupCleanupFallsBackToPID(t *testing.T) {
	t.Parallel()
	cmd := exec.Command("cmd.exe", "/C", "ping -n 30 127.0.0.1 > NUL")
	if err := cmd.Start(); err != nil {
		t.Skipf("cmd.exe not available: %v", err)
	}
	pid := cmd.Process.Pid
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
	if err := killProcessGroupByID(pid); err != nil {
		t.Fatalf("killProcessGroupByID: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		alive, err := processGroupAlive(pid)
		if err != nil {
			t.Fatalf("processGroupAlive: %v", err)
		}
		if !alive {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("process %d still alive after Windows process-group cleanup fallback", pid)
}
