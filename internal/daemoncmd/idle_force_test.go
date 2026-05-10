package daemoncmd

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shinpr/galley/internal/daemonctl"
)

func TestIdleTimeoutFlagDefault(t *testing.T) {
	t.Parallel()
	cmd := NewCommand("daemon")
	flag := cmd.PersistentFlags().Lookup("idle-timeout")
	if flag == nil {
		t.Fatal("idle-timeout flag is not registered")
	}
	if flag.DefValue != "10m0s" {
		t.Fatalf("idle-timeout default got %q, want 10m0s", flag.DefValue)
	}
}

func TestStopForceFlagRegistered(t *testing.T) {
	t.Parallel()
	cmd := NewCommand("daemon")
	for _, sub := range cmd.Commands() {
		if sub.Name() != "stop" {
			continue
		}
		if sub.Flags().Lookup("force") == nil {
			t.Fatal("stop --force flag is not registered")
		}
		return
	}
	t.Fatal("stop subcommand not found")
}

func TestStopForceWithoutDaemonReportsNotRunning(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	cmd := NewCommand("daemon")
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--root", root, "stop", "--force"})
	if err := cmd.Execute(); !errors.Is(err, daemonctl.ErrNotRunning) {
		t.Fatalf("expected ErrNotRunning, got %v", err)
	}
}

// startUnresponsiveDaemon spawns a verifiable stand-in daemon that ignores
// SIGTERM, writes its PID file with a fresh heartbeat so identity verification
// passes, and returns the workflow root and PID file path.
func startUnresponsiveDaemon(t *testing.T) (root, pidFile string, pid int) {
	t.Helper()
	shPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not available")
	}
	cmd := exec.Command(shPath, "-c", `trap "" TERM; sleep 30`)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	root = t.TempDir()
	pidFile = filepath.Join(root, "galley-daemon.pid")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	meta := daemonctl.NewPIDFile(cmd.Process.Pid, exe, root, []string{exe}).WithToken("stop-test-token")
	if err := daemonctl.WritePID(pidFile, meta); err != nil {
		t.Fatal(err)
	}
	if err := daemonctl.Heartbeat(pidFile, meta); err != nil {
		t.Fatal(err)
	}
	status, err := daemonctl.Inspect(pidFile, root, exe)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if !status.Verified {
		t.Skip("process identity verification unavailable on this platform")
	}
	return root, pidFile, cmd.Process.Pid
}

func TestStopWithoutForceKeepsPIDFileOnTimeout(t *testing.T) {
	root, pidFile, _ := startUnresponsiveDaemon(t)
	cmd := NewCommand("daemon")
	var out, errBuf bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errBuf)
	cmd.SetArgs([]string{"--root", root, "--stop-timeout", "300ms", "stop"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected stop to time out against an unresponsive daemon")
	}
	if !strings.Contains(err.Error(), "did not stop") {
		t.Fatalf("error should report the stop timeout, got %v", err)
	}
	if _, statErr := os.Stat(pidFile); statErr != nil {
		t.Fatalf("PID file must be preserved when stop times out without --force: %v", statErr)
	}
}

func TestStopForceKillsUnresponsiveDaemonAndRemovesPIDFile(t *testing.T) {
	root, pidFile, _ := startUnresponsiveDaemon(t)
	cmd := NewCommand("daemon")
	var out, errBuf bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errBuf)
	cmd.SetArgs([]string{"--root", root, "--stop-timeout", "300ms", "stop", "--force"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("force stop: %v", err)
	}
	if !strings.Contains(out.String(), "force stopped") {
		t.Fatalf("expected force-stop confirmation, got %q", out.String())
	}
	if _, statErr := os.Stat(pidFile); !os.IsNotExist(statErr) {
		t.Fatalf("PID file must be removed after a successful force stop, err=%v", statErr)
	}
}
