//go:build darwin || linux

package daemoncmd

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shinpr/galley/internal/daemonctl"
	"github.com/shinpr/galley/internal/proc"
)

// startStaleDaemon writes a daemon PID file whose recorded PID has already
// exited, mimicking a crashed daemon that left a stale record behind. The
// short-lived process is reaped before the PID file is written so
// daemonctl.Inspect reports the daemon as not alive. It returns the workflow
// root and PID file path.
func startStaleDaemon(t *testing.T) (root, pidFile string) {
	t.Helper()
	truePath, err := exec.LookPath("true")
	if err != nil {
		t.Skip("true not available")
	}
	cmd := exec.Command(truePath)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start short-lived process: %v", err)
	}
	deadPID := cmd.Process.Pid
	_, _ = cmd.Process.Wait()
	if alive, _ := daemonctl.Alive(deadPID); alive {
		t.Skipf("pid %d unexpectedly still alive after exit", deadPID)
	}
	root = t.TempDir()
	pidFile = filepath.Join(root, "galley-daemon.pid")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	meta := daemonctl.NewPIDFile(deadPID, exe, root, []string{exe}).WithToken("stale-test-token")
	if err := daemonctl.WritePID(pidFile, meta); err != nil {
		t.Fatal(err)
	}
	status, err := daemonctl.Inspect(pidFile, root, exe)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if status.Alive {
		t.Skipf("pid %d unexpectedly reported alive by Inspect", deadPID)
	}
	return root, pidFile
}

// TestStopForceWithStaleDaemonCleansRegisteredChildGroup covers the stale-
// record path of galley daemon stop --force: when the PID file names a daemon
// that has exited but a child process group it started is still recorded as
// alive, force stop must SIGKILL that group, confirm it is gone, and only then
// remove the PID file and clear the child registry.
func TestStopForceWithStaleDaemonCleansRegisteredChildGroup(t *testing.T) {
	root, pidFile := startStaleDaemon(t)
	childPID, _, _ := spawnTrackedChild(t, root, true)

	cmd := NewCommand("daemon")
	var out, errBuf bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errBuf)
	cmd.SetArgs([]string{"--root", root, "--stop-timeout", "2s", "stop", "--force"})
	if err := cmd.Execute(); !errors.Is(err, daemonctl.ErrNotRunning) {
		t.Fatalf("expected ErrNotRunning after cleaning a stale record, got %v (stderr=%s)", err, errBuf.String())
	}
	if alive, _ := daemonctl.Alive(childPID); alive {
		t.Fatal("registered child still alive after stale-record force stop returned")
	}
	if _, statErr := os.Stat(pidFile); !os.IsNotExist(statErr) {
		t.Fatalf("PID file must be removed only after the stale-record child cleanup succeeds, stat err=%v", statErr)
	}
	if _, statErr := os.Stat(proc.ChildRegistryPath(root)); !os.IsNotExist(statErr) {
		t.Fatalf("child registry must be cleared after successful cleanup, stat err=%v", statErr)
	}
}

// TestStopForceWithStaleDaemonCleanupFailureKeepsPIDFile covers the failure
// path of the stale-record cleanup: when a registered child process group
// cannot be confirmed gone, force stop must surface a visible error naming the
// surviving PID/PGID and leave the PID file in place so a follow-up operator
// action targets the same daemon record. As elsewhere, the SIGKILL'd child is
// left unreaped so its pgid still answers signal(0) — the "could not confirm
// exit" condition.
func TestStopForceWithStaleDaemonCleanupFailureKeepsPIDFile(t *testing.T) {
	root, pidFile := startStaleDaemon(t)
	childPID, childPGID, _ := spawnTrackedChild(t, root, false)

	cmd := NewCommand("daemon")
	var out, errBuf bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errBuf)
	cmd.SetArgs([]string{"--root", root, "--stop-timeout", "300ms", "stop", "--force"})
	err := cmd.Execute()
	if err == nil || errors.Is(err, daemonctl.ErrNotRunning) {
		t.Fatalf("expected a child-cleanup-incomplete error, got %v", err)
	}
	stderr := errBuf.String()
	wantPID := fmt.Sprintf("pid=%d", childPID)
	wantPGID := fmt.Sprintf("pgid=%d", childPGID)
	if !strings.Contains(stderr, wantPID) || !strings.Contains(stderr, wantPGID) {
		t.Fatalf("stderr %q must name the surviving %s and %s", stderr, wantPID, wantPGID)
	}
	if _, statErr := os.Stat(pidFile); statErr != nil {
		t.Fatalf("PID file must be preserved when stale-record child cleanup is incomplete: %v", statErr)
	}
}
