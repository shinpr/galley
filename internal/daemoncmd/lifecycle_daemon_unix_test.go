//go:build !windows

package daemoncmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/shinpr/galley/internal/daemonctl"
	"github.com/shinpr/galley/internal/queue"
	"github.com/shinpr/galley/internal/task"
	"github.com/shinpr/galley/internal/taskstate"
)

// lifecycleDaemonEnv is set when the test binary re-execs itself as a
// controllable daemon fixture for stop-idempotency lifecycle tests.
const lifecycleDaemonEnv = "GALLEY_TEST_LIFECYCLE_DAEMON"

// runLifecycleDaemon implements a production-shaped daemon fixture:
// verified PID file + heartbeat, an active running task with owner sidecar,
// first SIGTERM/SIGINT cancels and publishes terminal/review state with owner
// cleanup, second signal exits immediately (the double-stop failure mode).
func runLifecycleDaemon() int {
	root := os.Getenv("GALLEY_TEST_ROOT")
	pidFile := os.Getenv("GALLEY_TEST_PID_FILE")
	token := os.Getenv("GALLEY_TEST_TOKEN")
	readyFile := os.Getenv("GALLEY_TEST_READY_FILE")
	countFile := os.Getenv("GALLEY_TEST_COUNT_FILE")
	if root == "" || pidFile == "" || token == "" || readyFile == "" {
		fmt.Fprintln(os.Stderr, "lifecycle daemon: missing required env")
		return 2
	}

	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "lifecycle daemon: executable: %v\n", err)
		return 2
	}
	meta := daemonctl.NewPIDFile(os.Getpid(), exe, root, []string{exe, "lifecycle-daemon"}).WithToken(token)
	if err := daemonctl.WritePID(pidFile, meta); err != nil {
		fmt.Fprintf(os.Stderr, "lifecycle daemon: write pid: %v\n", err)
		return 2
	}
	if err := daemonctl.Heartbeat(pidFile, meta); err != nil {
		fmt.Fprintf(os.Stderr, "lifecycle daemon: heartbeat: %v\n", err)
		return 2
	}
	// Stop heartbeat before RemovePID so a late Heartbeat cannot recreate the
	// PID file after shutdown cleanup (matches production daemon RunE order).
	stopHeartbeat := startPIDHeartbeat(pidFile, meta)
	defer func() {
		stopHeartbeat()
		_ = daemonctl.RemovePID(pidFile, meta)
	}()

	if err := queue.EnsureLayout(root); err != nil {
		fmt.Fprintf(os.Stderr, "lifecycle daemon: layout: %v\n", err)
		return 2
	}
	runningPath := task.TaskStatePath(root, task.WorkflowStateRunning, "task-stop-lifecycle.yaml")
	if err := writeLifecycleRunningTask(runningPath); err != nil {
		fmt.Fprintf(os.Stderr, "lifecycle daemon: write task: %v\n", err)
		return 2
	}
	owner := queue.Owner{
		PID:        os.Getpid(),
		RecordedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if info, err := daemonctl.ProcessInfo(os.Getpid()); err == nil {
		owner.ProcessStartedAt = info.StartedAt
	}
	if err := queue.WriteOwner(runningPath, owner); err != nil {
		fmt.Fprintf(os.Stderr, "lifecycle daemon: write owner: %v\n", err)
		return 2
	}
	// Mirror daemon claim cleanup: owner sidecar is removed when the attempt ends.
	ownerRemoved := false
	removeOwner := func() {
		if ownerRemoved {
			return
		}
		_ = queue.RemoveOwner(runningPath)
		ownerRemoved = true
	}
	defer removeOwner()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)

	signalCount := 0
	done := make(chan struct{})
	go func() {
		select {
		case <-signals:
			signalCount++
			recordLifecycleSignalCount(countFile, signalCount)
			// First signal: request graceful shutdown; active attempt may finish.
			cancel()
		case <-done:
			return
		}
		select {
		case <-signals:
			signalCount++
			recordLifecycleSignalCount(countFile, signalCount)
			// Second signal: production daemon exits immediately without waiting
			// for active attempts or deferred cleanup.
			os.Exit(130)
		case <-done:
		}
	}()

	if err := os.WriteFile(readyFile, []byte("ready\n"), 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "lifecycle daemon: ready: %v\n", err)
		return 2
	}

	// Controllable active attempt: block until graceful cancel, then publish
	// the existing shutdown terminal/review state used by the real daemon.
	<-ctx.Done()
	close(done)

	loaded, err := task.Load(runningPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "lifecycle daemon: load task: %v\n", err)
		return 1
	}
	loaded.Status = task.StatusNeedsSupervisorReview
	loaded.Risks = append(loaded.Risks, task.Risk{
		ID:                   fmt.Sprintf("shutdown-%d", time.Now().UTC().UnixNano()),
		Type:                 "partial_verification",
		Detail:               "Shutdown was requested while an attempt was active; Galley published terminal review state instead of leaving a running claim.",
		Mitigation:           "Review the run evidence and requeue the task when ready.",
		HumanReviewSuggested: true,
	})
	if err := taskstate.MoveToStatus(root, runningPath, &loaded); err != nil {
		fmt.Fprintf(os.Stderr, "lifecycle daemon: publish terminal state: %v\n", err)
		return 1
	}
	removeOwner()
	return 0
}

func writeLifecycleRunningTask(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	body := `id: "task-stop-lifecycle"
mode: "afk"
status: "running"
goal: "Lifecycle stop idempotency fixture."
acceptance_criteria:
  - id: "AC1"
    text: "Runs."
    verification: "true"
    status: "pending"
scope:
  cwd: "."
  allowed_paths:
    - "."
  forbidden_paths: []
  permission: "edit"
execution_policy:
  loop_budget: 1
  timeout_ms: 5000
  afk_decision_policy: "choose-smallest-reversible"
  stop_on_destructive_operation: true
  stop_on_missing_secret: false
  stop_on_external_service_unavailable: false
worktree:
  enabled: false
  branch: ""
  path: ""
supervisor:
  review_iterations: 0
executor:
  cli: "claude"
  model: "opus"
  effort: "high"
  prompt_profile: "codexized-claude-executor-v1"
  prompt_mode: "replace"
decisions: []
risks: []
attempts: []
verification:
  commands: []
pr:
  url: ""
  status: ""
`
	return os.WriteFile(path, []byte(body), 0o600)
}

func recordLifecycleSignalCount(path string, count int) {
	if path == "" {
		return
	}
	_ = os.WriteFile(path, []byte(strconv.Itoa(count)+"\n"), 0o600)
}
