package daemonctl

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"
)

func TestForceStopGracefulSucceedsWithoutKill(t *testing.T) {
	t.Parallel()
	sleepPath, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("sleep not available")
	}
	cmd := exec.Command(sleepPath, "10")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
	meta := NewPIDFile(cmd.Process.Pid, sleepPath, t.TempDir(), []string{sleepPath})
	if !Verify(meta, meta.Root, meta.Executable) {
		t.Skip("process identity verification unavailable on this platform")
	}
	forced, err := ForceStop(meta, 2*time.Second, "")
	if err != nil {
		t.Fatalf("force stop: %v", err)
	}
	if forced {
		t.Fatal("graceful stop should not escalate to force kill")
	}
	alive, err := Alive(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	if alive {
		t.Fatal("daemon still alive after graceful stop")
	}
}

func TestForceStopKillsUnresponsiveDaemonAfterTimeout(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		// Windows Stop is an immediate TerminateProcess with no graceful phase,
		// so a process cannot ignore shutdown to force the escalation-after-
		// timeout path this test exercises.
		t.Skip("no graceful-stop escalation path on Windows (Stop is TerminateProcess)")
	}
	shPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not available")
	}
	// Ignore SIGTERM so graceful stop times out and the verified force kill runs.
	cmd := exec.Command(shPath, "-c", `trap "" TERM; sleep 10`)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
	meta := NewPIDFile(cmd.Process.Pid, shPath, t.TempDir(), []string{shPath})
	if !Verify(meta, meta.Root, meta.Executable) {
		t.Skip("process identity verification unavailable on this platform")
	}
	forced, err := ForceStop(meta, 500*time.Millisecond, "")
	if err != nil {
		t.Fatalf("force stop: %v", err)
	}
	if !forced {
		t.Fatal("expected force kill after graceful stop timeout")
	}
	alive, err := Alive(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	if alive {
		t.Fatal("daemon survived force kill")
	}
}

func TestKillVerifiedRejectsUnverifiedMeta(t *testing.T) {
	t.Parallel()
	// Use this process PID with a contradictory executable and no start-identity
	// fence: KillVerified must not authorize SIGKILL from Alive alone. Clearing
	// ProcessStartedAt prevents the shell-wrapper start-identity trust path from
	// treating the live test binary as a verified daemon under a fake path.
	meta := NewPIDFile(os.Getpid(), "/nonexistent/galley-impostor", t.TempDir(), []string{"/nonexistent/galley-impostor"})
	meta.ProcessStartedAt = ""
	if err := KillVerified(meta, time.Second); !errors.Is(err, ErrUnverifiedProcess) {
		t.Fatalf("expected ErrUnverifiedProcess, got %v", err)
	}
	alive, err := Alive(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if !alive {
		t.Fatal("KillVerified must not kill the caller on impostor metadata")
	}
}

// TestKillVerifiedRejectsFreshHeartbeatWhenProcessInfoUnavailable proves force
// kill fails closed: Verify would accept a fresh heartbeat, but KillVerified
// requires process-start identity and must not SIGKILL an unverifiable PID.
func TestKillVerifiedRejectsFreshHeartbeatWhenProcessInfoUnavailable(t *testing.T) {
	// Not parallel: swaps processInfoForTargetHook.
	meta, pidFile, cleanup := startStoppableDaemon(t)
	defer cleanup()
	if err := Heartbeat(pidFile, meta); err != nil {
		t.Fatal(err)
	}
	// Refresh heartbeat timestamp onto meta used for kill identity.
	disk, err := ReadPIDFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	disk.Token = meta.Token
	if !Verify(disk, disk.Root, disk.Executable) {
		t.Fatal("precondition: fresh heartbeat should satisfy Verify")
	}

	prev := processInfoForTargetHook
	processInfoForTargetHook = func(int) (ProcessInfoResult, error) {
		return ProcessInfoResult{}, errors.New("process metadata unavailable")
	}
	defer func() { processInfoForTargetHook = prev }()

	if err := KillVerified(disk, time.Second); !errors.Is(err, ErrUnverifiedProcess) {
		t.Fatalf("expected ErrUnverifiedProcess when ProcessInfo unavailable, got %v", err)
	}
	alive, err := Alive(disk.PID)
	if err != nil {
		t.Fatal(err)
	}
	if !alive {
		t.Fatal("KillVerified must not kill when process identity is unverifiable")
	}
}

func TestStopVerifiedRejectsUnverifiedMeta(t *testing.T) {
	t.Parallel()
	meta := NewPIDFile(os.Getpid(), "/nonexistent/galley-impostor", t.TempDir(), []string{"/nonexistent/galley-impostor"})
	if err := StopVerified(meta, time.Second); !errors.Is(err, ErrUnverifiedProcess) {
		t.Fatalf("expected ErrUnverifiedProcess, got %v", err)
	}
}
