package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/shinpr/galley/internal/daemonconfig"
	"github.com/shinpr/galley/internal/task"
)

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
		Command: "cat > " + marker,
	}}
	notifyTerminalPublication(context.Background(), opts, filepath.Join(root, "tasks", "running", "task-a.yaml"), &runDir)
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
	opts := Options{Root: root, Notifications: &daemonconfig.NotificationConfig{Enabled: true, Command: "touch " + marker}}
	notifyTerminalPublication(context.Background(), opts, filepath.Join(root, "tasks", "running", "task-b.yaml"), nil)
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("expected hook to fire for needs_supervisor_review: %v", err)
	}
}

// AC2: accepted is opt-in; with default `on` it must NOT fire.
func TestNotifyTerminalPublicationSkipsNonDefaultStatus(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(t.TempDir(), "fired")
	writePublishedTask(t, root, "done", "task-c.yaml", baseTask("task-c", "accepted"))
	opts := Options{Root: root, Notifications: &daemonconfig.NotificationConfig{Enabled: true, Command: "touch " + marker}}
	notifyTerminalPublication(context.Background(), opts, filepath.Join(root, "tasks", "running", "task-c.yaml"), nil)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("hook fired for accepted under default on-list; expected skip")
	}
}

// AC2: opt-in accepted fires when configured.
func TestNotifyTerminalPublicationFiresOnOptInAccepted(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(t.TempDir(), "fired")
	writePublishedTask(t, root, "done", "task-d.yaml", baseTask("task-d", "accepted"))
	opts := Options{Root: root, Notifications: &daemonconfig.NotificationConfig{Enabled: true, On: []string{"accepted"}, Command: "touch " + marker}}
	notifyTerminalPublication(context.Background(), opts, filepath.Join(root, "tasks", "running", "task-d.yaml"), nil)
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("expected hook to fire for opt-in accepted: %v", err)
	}
}

// AC2: a task still in running/ (failed terminal move) produces no published
// file, so no notification fires.
func TestNotifyTerminalPublicationSkipsWhenNotPublished(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(t.TempDir(), "fired")
	opts := Options{Root: root, Notifications: &daemonconfig.NotificationConfig{Enabled: true, Command: "touch " + marker}}
	notifyTerminalPublication(context.Background(), opts, filepath.Join(root, "tasks", "running", "missing.yaml"), nil)
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
	notifyTerminalPublication(context.Background(), opts, filepath.Join(root, "tasks", "running", "task-e.yaml"), nil)
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
	notifyTerminalPublication(context.Background(), Options{Root: root}, filepath.Join(root, "tasks", "running", "task-f.yaml"), nil)
	// disabled config
	notifyTerminalPublication(context.Background(), Options{Root: root, Notifications: &daemonconfig.NotificationConfig{Enabled: false, Command: "touch " + marker}}, filepath.Join(root, "tasks", "running", "task-f.yaml"), nil)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("disabled/nil notifications fired a hook")
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
