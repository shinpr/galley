//go:build !windows

package daemoncmd

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shinpr/galley/internal/daemonctl"
	"github.com/shinpr/galley/internal/queue"
	"github.com/shinpr/galley/internal/task"
)

// TestRepeatedStopCommandsSendOneGracefulSignal covers AC1 at the CLI boundary:
// concurrent `galley daemon stop` against the same verified daemon must converge
// on stopped without a second graceful signal.
func TestRepeatedStopCommandsSendOneGracefulSignal(t *testing.T) {
	root, pidFile, countFile, cleanup := startSignalCountingDaemon(t)
	defer cleanup()

	const callers = 3
	type result struct {
		err    error
		stdout string
	}
	results := make(chan result, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := NewCommand("daemon")
			var out, errBuf bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&errBuf)
			cmd.SetArgs([]string{"--root", root, "--stop-timeout", "5s", "stop"})
			results <- result{err: cmd.Execute(), stdout: out.String()}
		}()
	}
	wg.Wait()
	close(results)

	for res := range results {
		if res.err != nil {
			t.Fatalf("every cooperating stop must converge on stopped, got err=%v stdout=%q", res.err, res.stdout)
		}
		if !strings.Contains(res.stdout, "stopped") {
			t.Fatalf("expected stopped confirmation, got %q", res.stdout)
		}
	}

	// Allow the trap handler to flush the count file.
	deadline := time.Now().Add(2 * time.Second)
	var count string
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(countFile)
		if err == nil {
			count = strings.TrimSpace(string(data))
			if count != "" {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if count != "1" {
		t.Fatalf("graceful SIGTERM count got %q, want 1", count)
	}
	if _, err := os.Stat(pidFile); !os.IsNotExist(err) {
		t.Fatalf("PID file should be removed after stop, err=%v", err)
	}
}

// TestForceStopDuringGracefulCLIStopKillsDaemon covers AC3: explicit --force
// during an in-progress normal stop still terminates the verified daemon.
func TestForceStopDuringGracefulCLIStopKillsDaemon(t *testing.T) {
	root, pidFile, _ := startUnresponsiveDaemon(t)

	normalDone := make(chan error, 1)
	go func() {
		cmd := NewCommand("daemon")
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs([]string{"--root", root, "--stop-timeout", "2s", "stop"})
		normalDone <- cmd.Execute()
	}()

	// Wait until normal stop has claimed coordination.
	coord := daemonctl.StopCoordinationPath(pidFile)
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(coord); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for normal stop coordination")
		}
		time.Sleep(10 * time.Millisecond)
	}

	forceCmd := NewCommand("daemon")
	var out, errBuf bytes.Buffer
	forceCmd.SetOut(&out)
	forceCmd.SetErr(&errBuf)
	forceCmd.SetArgs([]string{"--root", root, "--stop-timeout", "300ms", "stop", "--force"})
	if err := forceCmd.Execute(); err != nil {
		t.Fatalf("force stop: %v (stderr=%s)", err, errBuf.String())
	}
	if !strings.Contains(out.String(), "force stopped") && !strings.Contains(out.String(), "stopped") {
		t.Fatalf("expected force/stop confirmation, got %q", out.String())
	}
	select {
	case <-normalDone:
	case <-time.After(3 * time.Second):
		t.Fatal("normal stop did not return after force")
	}
	if _, err := os.Stat(pidFile); !os.IsNotExist(err) {
		t.Fatalf("PID file should be removed after force stop, err=%v", err)
	}
}

// TestActiveTaskTwoCLIStopsPublishTerminalState covers AC2: a daemon with an
// active claimed task receives exactly one graceful signal from two concurrent
// CLI stop callers, both callers observe stopped, and the task publishes the
// existing shutdown terminal/review state with owner-sidecar cleanup.
func TestActiveTaskTwoCLIStopsPublishTerminalState(t *testing.T) {
	root, pidFile, countFile, runningPath, cleanup := startLifecycleDaemon(t)
	defer cleanup()

	const callers = 2
	type result struct {
		err    error
		stdout string
	}
	results := make(chan result, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := NewCommand("daemon")
			var out, errBuf bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&errBuf)
			cmd.SetArgs([]string{"--root", root, "--stop-timeout", "10s", "stop"})
			results <- result{err: cmd.Execute(), stdout: out.String()}
		}()
	}
	wg.Wait()
	close(results)

	for res := range results {
		if res.err != nil {
			t.Fatalf("cooperating stop must converge on stopped, got err=%v stdout=%q", res.err, res.stdout)
		}
		if !strings.Contains(res.stdout, "stopped") {
			t.Fatalf("expected stopped confirmation, got %q", res.stdout)
		}
	}

	deadline := time.Now().Add(3 * time.Second)
	var count string
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(countFile)
		if err == nil {
			count = strings.TrimSpace(string(data))
			if count == "1" {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if count != "1" {
		t.Fatalf("graceful SIGTERM count got %q, want 1", count)
	}
	if _, err := os.Stat(pidFile); !os.IsNotExist(err) {
		t.Fatalf("PID file should be removed after stop, err=%v", err)
	}
	if _, err := os.Stat(runningPath); !os.IsNotExist(err) {
		t.Fatalf("running task YAML must be removed after graceful stop, err=%v", err)
	}
	if _, err := os.Stat(queue.OwnerPath(runningPath)); !os.IsNotExist(err) {
		t.Fatalf("owner sidecar must be removed after graceful stop, err=%v", err)
	}
	failedPath := task.TaskStatePath(root, task.WorkflowStateFailed, filepath.Base(runningPath))
	failedTask, err := task.Load(failedPath)
	if err != nil {
		t.Fatalf("load published terminal task: %v", err)
	}
	if failedTask.Status != task.StatusNeedsSupervisorReview {
		t.Fatalf("status got %q, want %q", failedTask.Status, task.StatusNeedsSupervisorReview)
	}
	if len(failedTask.Risks) == 0 || !strings.Contains(failedTask.Risks[len(failedTask.Risks)-1].ID, "shutdown-") {
		t.Fatalf("shutdown risk missing: %#v", failedTask.Risks)
	}
}

// startSignalCountingDaemon spawns a process that counts SIGTERM deliveries and
// exits after the first signal (graceful stand-in). A second signal would
// increment the counter, which is the CLI double-stop failure mode.
func startSignalCountingDaemon(t *testing.T) (root, pidFile, countFile string, cleanup func()) {
	t.Helper()
	shPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not available")
	}
	root = t.TempDir()
	countFile = filepath.Join(root, "term-count")
	// shellcheck-style: count TERM, exit on first (graceful). Second TERM would
	// still run the trap before exit and bump the count — the AC1 failure mode.
	script := `count=0
trap 'count=$((count+1)); printf "%s\n" "$count" > "$1"; exit 0' TERM
while true; do sleep 0.05; done`
	cmd := exec.Command(shPath, "-c", script, "count-daemon", countFile)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	cleanup = func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}
	pidFile = filepath.Join(root, "galley-daemon.pid")
	// Bind PID metadata to the real process executable (sh) so verification
	// can use ProcessInfo identity rather than only the heartbeat bypass.
	meta := daemonctl.NewPIDFile(cmd.Process.Pid, shPath, root, []string{shPath, "-c", script}).WithToken("idempotent-stop")
	if err := daemonctl.WritePID(pidFile, meta); err != nil {
		cleanup()
		t.Fatal(err)
	}
	if err := daemonctl.Heartbeat(pidFile, meta); err != nil {
		cleanup()
		t.Fatal(err)
	}
	// CLI stop verifies against os.Executable() (the test binary). Align the
	// PID record with that expected executable while keeping a fresh heartbeat
	// so Inspect marks the stand-in verified under the CLI identity contract.
	exe, err := os.Executable()
	if err != nil {
		cleanup()
		t.Fatal(err)
	}
	meta = daemonctl.NewPIDFile(cmd.Process.Pid, exe, root, []string{exe}).WithToken("idempotent-stop")
	if err := daemonctl.WritePID(pidFile, meta); err != nil {
		cleanup()
		t.Fatal(err)
	}
	if err := daemonctl.Heartbeat(pidFile, meta); err != nil {
		cleanup()
		t.Fatal(err)
	}
	status, err := daemonctl.Inspect(pidFile, root, exe)
	if err != nil {
		cleanup()
		t.Fatalf("inspect: %v", err)
	}
	if !status.Verified {
		cleanup()
		t.Skip("process identity verification unavailable on this platform")
	}
	return root, pidFile, countFile, cleanup
}

// startLifecycleDaemon re-execs the package test binary as a controllable
// daemon that owns a running task claim and implements the production
// dual-signal contract. Identity matches os.Executable() so CLI stop Verify
// succeeds without spoofing a foreign executable.
func startLifecycleDaemon(t *testing.T) (root, pidFile, countFile, runningPath string, cleanup func()) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root = t.TempDir()
	pidFile = filepath.Join(root, "galley-daemon.pid")
	countFile = filepath.Join(root, "term-count")
	readyFile := filepath.Join(root, "ready")
	runningPath = task.TaskStatePath(root, task.WorkflowStateRunning, "task-stop-lifecycle.yaml")
	token := "lifecycle-stop-token"

	cmd := exec.Command(exe)
	cmd.Env = append(os.Environ(),
		lifecycleDaemonEnv+"=1",
		"GALLEY_TEST_ROOT="+root,
		"GALLEY_TEST_PID_FILE="+pidFile,
		"GALLEY_TEST_TOKEN="+token,
		"GALLEY_TEST_READY_FILE="+readyFile,
		"GALLEY_TEST_COUNT_FILE="+countFile,
	)
	var daemonLog bytes.Buffer
	cmd.Stdout = &daemonLog
	cmd.Stderr = &daemonLog
	if err := cmd.Start(); err != nil {
		t.Fatalf("start lifecycle daemon: %v", err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	cleanup = func() {
		_ = cmd.Process.Kill()
		select {
		case <-exited:
		case <-time.After(2 * time.Second):
		}
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(readyFile); err == nil {
			break
		}
		select {
		case err := <-exited:
			t.Fatalf("lifecycle daemon exited before ready: %v\nlog:\n%s", err, daemonLog.String())
		default:
		}
		if time.Now().After(deadline) {
			cleanup()
			t.Fatalf("timed out waiting for lifecycle daemon ready marker\nlog:\n%s", daemonLog.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := os.Stat(runningPath); err != nil {
		cleanup()
		t.Fatalf("running task missing after ready: %v", err)
	}
	if _, err := os.Stat(queue.OwnerPath(runningPath)); err != nil {
		cleanup()
		t.Fatalf("owner sidecar missing after ready: %v", err)
	}
	status, err := daemonctl.Inspect(pidFile, root, exe)
	if err != nil {
		cleanup()
		t.Fatalf("inspect lifecycle daemon: %v", err)
	}
	if !status.Alive || !status.Verified {
		cleanup()
		t.Fatalf("lifecycle daemon not verified alive: alive=%v verified=%v", status.Alive, status.Verified)
	}
	return root, pidFile, countFile, runningPath, cleanup
}
