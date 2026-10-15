//go:build darwin || linux

package daemoncmd

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shinpr/galley/internal/daemonctl"
	"github.com/shinpr/galley/internal/queue"
	"github.com/shinpr/galley/internal/task"
)

// startUnresponsiveDaemon spawns a verifiable stand-in daemon that ignores
// SIGTERM, writes its PID file with a fresh heartbeat so identity verification
// passes, and returns the workflow root and PID file path.
func startUnresponsiveDaemon(t *testing.T) (root, pidFile string, pid int) {
	t.Helper()
	shPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not available")
	}
	cmd := exec.CommandContext(t.Context(), shPath, "-c", `trap "" TERM; printf ready; exec sleep 30`)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	ready := make([]byte, len("ready"))
	if _, err := io.ReadFull(stdout, ready); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("wait for unresponsive daemon readiness: %v", err)
	}
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-done
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
	if !strings.Contains(err.Error(), "shutdown remains in progress") || !strings.Contains(err.Error(), "interrupts active attempts") {
		t.Fatalf("error should explain shutdown state and force-stop consequences, got %v", err)
	}
	if _, statErr := os.Stat(pidFile); statErr != nil {
		t.Fatalf("PID file must be preserved when stop times out without --force: %v", statErr)
	}
}

func TestStopForceRetriesTaskPublicationAfterDaemonStops(t *testing.T) {
	root, pidFile, daemonPID := startUnresponsiveDaemon(t)
	if err := queue.EnsureLayout(root); err != nil {
		t.Fatal(err)
	}
	runningPath := task.TaskStatePath(root, task.WorkflowStateRunning, "task.yaml")
	if err := os.WriteFile(runningPath, []byte("status: ["), 0o600); err != nil {
		t.Fatal(err)
	}
	owner := queue.Owner{PID: daemonPID, RecordedAt: "now"}
	if err := queue.WriteOwner(runningPath, owner); err != nil {
		t.Fatal(err)
	}

	first := NewCommand("daemon")
	first.SetOut(&bytes.Buffer{})
	first.SetErr(&bytes.Buffer{})
	first.SetArgs([]string{"--root", root, "--stop-timeout", "300ms", "stop", "--force"})
	if err := first.Execute(); err == nil {
		t.Fatal("expected initial task publication error")
	}
	for _, evidence := range []string{pidFile, runningPath, queue.OwnerPath(runningPath)} {
		if _, err := os.Stat(evidence); err != nil {
			t.Fatalf("evidence %s was not preserved after failed publication: %v", evidence, err)
		}
	}
	if err := task.Save(runningPath, task.Task{ID: "task", Status: task.StatusRunning}); err != nil {
		t.Fatal(err)
	}

	second := NewCommand("daemon")
	var out bytes.Buffer
	second.SetOut(&out)
	second.SetErr(&bytes.Buffer{})
	second.SetArgs([]string{"--root", root, "--stop-timeout", "300ms", "stop", "--force"})
	if err := second.Execute(); err != nil {
		t.Fatalf("retry force stop: %v", err)
	}
	if !strings.Contains(out.String(), "force stopped") {
		t.Fatalf("expected force-stop confirmation, got %q", out.String())
	}
	if _, err := os.Stat(pidFile); !os.IsNotExist(err) {
		t.Fatalf("PID file remains after recovered publication: %v", err)
	}
	failedPath := task.TaskStatePath(root, task.WorkflowStateFailed, filepath.Base(runningPath))
	failed, err := task.Load(failedPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(failed.Attempts) != 1 || failed.Attempts[0].Error == nil || failed.Attempts[0].Error.Kind != "daemon_force_stopped" {
		t.Fatalf("force-stop evidence = %#v", failed.Attempts)
	}
}
