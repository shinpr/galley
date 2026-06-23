package daemon

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shinpr/galley/internal/daemonconfig"
	"github.com/shinpr/galley/internal/task"
)

func shellPath(path string) string {
	return "'" + strings.ReplaceAll(filepath.ToSlash(path), "'", `'\''`) + "'"
}

// trackNotifications installs a WaitGroup so a test can await the asynchronous
// delivery goroutines started by notifyTerminalPublication. The global seam is
// reset on cleanup. These tests do not run in parallel, and notifyTerminalPublication
// only reads the seam after confirming a configured hook, so unrelated daemon
// tests (which leave Notifications nil) never touch it.
func trackNotifications(t *testing.T) *sync.WaitGroup {
	t.Helper()
	wg := &sync.WaitGroup{}
	notifyDeliveryTracking = wg
	t.Cleanup(func() {
		wg.Wait()
		notifyDeliveryTracking = nil
	})
	return wg
}

func writePublishedTask(t *testing.T, root, state, base string, tk task.Task) string {
	t.Helper()
	dir := filepath.Join(root, "tasks", state)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, base)
	if err := task.Save(p, tk); err != nil {
		t.Fatal(err)
	}
	return p
}

func baseTask(id, status string) task.Task {
	return task.Task{
		ID:       id,
		Mode:     "afk",
		Status:   status,
		Goal:     "do the thing",
		Scope:    task.Scope{CWD: "/repos/app"},
		Attempts: []task.Attempt{{Number: 1, Summary: "executor failed"}},
	}
}

// AC2 + AC5: a matching terminal status fires the hook exactly once; the marker
// file written by the hook proves the configured command ran with task data.
func TestNotifyTerminalPublicationFiresOnMatchingStatus(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(t.TempDir(), "fired")
	writePublishedTask(t, root, "failed", "task-a.yaml", baseTask("task-a", "failed"))
	runDir := "/galley/runs/run-1"
	opts := Options{Root: root, Notifications: &daemonconfig.NotificationConfig{
		Enabled: true,
		Command: "cat > " + shellPath(marker),
	}}
	deliverTerminalNotification(opts, "task-a.yaml", runDir)
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("hook did not run: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("hook ran but received no stdin payload")
	}
}

// AC2: needs_supervisor_review is a default-on status.
func TestNotifyTerminalPublicationFiresOnNeedsSupervisorReview(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(t.TempDir(), "fired")
	writePublishedTask(t, root, "failed", "task-b.yaml", baseTask("task-b", "needs_supervisor_review"))
	opts := Options{Root: root, Notifications: &daemonconfig.NotificationConfig{Enabled: true, Command: "touch " + shellPath(marker)}}
	deliverTerminalNotification(opts, "task-b.yaml", "")
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("expected hook to fire for needs_supervisor_review: %v", err)
	}
}

// AC2: accepted is opt-in; with default `on` it must NOT fire.
func TestNotifyTerminalPublicationSkipsNonDefaultStatus(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(t.TempDir(), "fired")
	writePublishedTask(t, root, "done", "task-c.yaml", baseTask("task-c", "accepted"))
	opts := Options{Root: root, Notifications: &daemonconfig.NotificationConfig{Enabled: true, Command: "touch " + shellPath(marker)}}
	deliverTerminalNotification(opts, "task-c.yaml", "")
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("hook fired for accepted under default on-list; expected skip")
	}
}

// AC2: opt-in accepted fires when configured.
func TestNotifyTerminalPublicationFiresOnOptInAccepted(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(t.TempDir(), "fired")
	writePublishedTask(t, root, "done", "task-d.yaml", baseTask("task-d", "accepted"))
	opts := Options{Root: root, Notifications: &daemonconfig.NotificationConfig{Enabled: true, On: []string{"accepted"}, Command: "touch " + shellPath(marker)}}
	deliverTerminalNotification(opts, "task-d.yaml", "")
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("expected hook to fire for opt-in accepted: %v", err)
	}
}

// AC2: a task still in running/ (failed terminal move) produces no published
// file, so no notification fires.
func TestNotifyTerminalPublicationSkipsWhenNotPublished(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(t.TempDir(), "fired")
	opts := Options{Root: root, Notifications: &daemonconfig.NotificationConfig{Enabled: true, Command: "touch " + shellPath(marker)}}
	deliverTerminalNotification(opts, "missing.yaml", "")
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("hook fired for an unpublished task; expected skip")
	}
}

// AC5: a failing hook command must not panic and must not alter task state.
func TestNotifyTerminalPublicationSwallowsHookFailure(t *testing.T) {
	root := t.TempDir()
	published := writePublishedTask(t, root, "failed", "task-e.yaml", baseTask("task-e", "failed"))
	opts := Options{Root: root, Notifications: &daemonconfig.NotificationConfig{Enabled: true, Command: "exit 7"}}
	// Must not panic.
	deliverTerminalNotification(opts, "task-e.yaml", "")
	// Task file remains exactly where it was published.
	if _, err := os.Stat(published); err != nil {
		t.Fatalf("published task disturbed by hook failure: %v", err)
	}
}

// Disabled or absent config never fires.
func TestNotifyTerminalPublicationDisabled(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(t.TempDir(), "fired")
	writePublishedTask(t, root, "failed", "task-f.yaml", baseTask("task-f", "failed"))
	// nil config
	deliverTerminalNotification(Options{Root: root}, "task-f.yaml", "")
	// disabled config
	deliverTerminalNotification(Options{Root: root, Notifications: &daemonconfig.NotificationConfig{Enabled: false, Command: "touch " + shellPath(marker)}}, "task-f.yaml", "")
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("disabled/nil notifications fired a hook")
	}
}

// Revision AC: a slow notification command must not delay the task-processing
// path that releases the daemon worker/iteration. notifyTerminalPublication
// dispatches delivery on a detached goroutine, so it returns immediately even
// though the configured command sleeps before writing its marker. Delivery still
// completes off the critical path.
func TestNotifyTerminalPublicationDoesNotBlockOnSlowCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell command")
	}
	root := t.TempDir()
	marker := filepath.Join(t.TempDir(), "fired")
	writePublishedTask(t, root, "failed", "task-slow.yaml", baseTask("task-slow", "failed"))
	opts := Options{Root: root, Notifications: &daemonconfig.NotificationConfig{
		Enabled: true,
		// Sleep well past the assertion window before delivering. If delivery were
		// synchronous, the call below would block for the full sleep.
		Command: "sleep 2; touch " + shellPath(marker),
	}}
	wg := trackNotifications(t)

	start := time.Now()
	notifyTerminalPublication(context.Background(), opts, filepath.Join(root, "tasks", "running", "task-slow.yaml"), nil)
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("notifyTerminalPublication blocked for %s; delivery must be asynchronous so the daemon worker/iteration is not delayed", elapsed)
	}
	// The slow command is still running, so its marker is not present yet: this
	// proves the call returned before delivery finished.
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("marker present before the slow command finished; delivery was not asynchronous")
	}
	// Best-effort delivery still completes once the detached goroutine finishes.
	wg.Wait()
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("asynchronous delivery did not complete: %v", err)
	}
}

// Revision AC: a stuck notification command must still be killed by the timeout
// rather than becoming an unmanaged long-running child. The call returns
// immediately (async), and the detached delivery goroutine finishes shortly
// after the bounded timeout instead of running for the full sleep.
func TestNotifyTerminalPublicationStuckCommandKilledByTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell command")
	}
	root := t.TempDir()
	writePublishedTask(t, root, "failed", "task-stuck.yaml", baseTask("task-stuck", "failed"))
	opts := Options{
		Root:          root,
		notifyTimeout: 200 * time.Millisecond,
		Notifications: &daemonconfig.NotificationConfig{Enabled: true, Command: "sleep 30"},
	}
	wg := trackNotifications(t)

	start := time.Now()
	notifyTerminalPublication(context.Background(), opts, filepath.Join(root, "tasks", "running", "task-stuck.yaml"), nil)
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("notifyTerminalPublication blocked for %s on a stuck command", elapsed)
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("stuck notification command was not killed by the timeout")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("stuck command not bounded by the timeout: elapsed %s", elapsed)
	}
}

func TestLatestTaskSummaryFallback(t *testing.T) {
	if got := latestTaskSummary(task.Task{Goal: "g"}); got != "g" {
		t.Fatalf("expected goal fallback, got %q", got)
	}
	rk := task.Task{Goal: "g", Risks: []task.Risk{{Detail: "risky"}}}
	if got := latestTaskSummary(rk); got != "risky" {
		t.Fatalf("expected risk fallback, got %q", got)
	}
}
