package daemonctl

import (
	"errors"
	"os"
	"os/exec"
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
	forced, err := ForceStop(meta, 2*time.Second)
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
	forced, err := ForceStop(meta, 500*time.Millisecond)
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
	meta := NewPIDFile(os.Getpid(), "/nonexistent/galley-impostor", t.TempDir(), []string{"/nonexistent/galley-impostor"})
	if err := KillVerified(meta, time.Second); !errors.Is(err, ErrUnverifiedProcess) {
		t.Fatalf("expected ErrUnverifiedProcess, got %v", err)
	}
}

func TestStopVerifiedRejectsUnverifiedMeta(t *testing.T) {
	t.Parallel()
	meta := NewPIDFile(os.Getpid(), "/nonexistent/galley-impostor", t.TempDir(), []string{"/nonexistent/galley-impostor"})
	if err := StopVerified(meta, time.Second); !errors.Is(err, ErrUnverifiedProcess) {
		t.Fatalf("expected ErrUnverifiedProcess, got %v", err)
	}
}
