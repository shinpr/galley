package daemoncmd

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/shinpr/galley/internal/daemonctl"
	"github.com/shinpr/galley/internal/queue"
	"github.com/shinpr/galley/internal/task"
)

func writeForceStopTask(t *testing.T, root, name string, owner *queue.Owner) string {
	t.Helper()
	if err := queue.EnsureLayout(root); err != nil {
		t.Fatal(err)
	}
	path := task.TaskStatePath(root, task.WorkflowStateRunning, name)
	loaded := task.Task{
		ID:     name,
		Status: task.StatusRunning,
		Worktree: task.Worktree{
			Enabled: true,
			Branch:  "agent/" + name,
			Path:    filepath.Join(root, "worktrees", name),
		},
		RevisionRequests: []task.RevisionRequest{{
			ID:     "supervisor-finding-1",
			Source: "supervisor",
			Text:   "preserve this request",
			Status: "pending",
		}},
		Attempts: []task.Attempt{{Number: 1, ClaudeStatus: "completed", SupervisorVerdict: "needs_revision"}},
	}
	if err := task.Save(path, loaded); err != nil {
		t.Fatal(err)
	}
	if owner != nil {
		if err := queue.WriteOwner(path, *owner); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func TestFailOwnedRunningTasksPublishesOnlyMatchingOwner(t *testing.T) {
	root := t.TempDir()
	target := daemonctl.PIDFile{PID: 101, ProcessStartedAt: "target-start"}
	matching := queue.Owner{PID: 101, ProcessStartedAt: "target-start", RecordedAt: "now"}
	otherDaemon := queue.Owner{PID: 202, ProcessStartedAt: "other-start", RecordedAt: "now"}
	recycledPID := queue.Owner{PID: 101, ProcessStartedAt: "old-start", RecordedAt: "now"}

	matchingPath := writeForceStopTask(t, root, "matching.yaml", &matching)
	otherPath := writeForceStopTask(t, root, "other.yaml", &otherDaemon)
	recycledPath := writeForceStopTask(t, root, "recycled.yaml", &recycledPID)
	missingPath := writeForceStopTask(t, root, "missing.yaml", nil)
	invalidPath := writeForceStopTask(t, root, "invalid.yaml", nil)
	if err := os.WriteFile(queue.OwnerPath(invalidPath), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := failOwnedRunningTasks(root, target); err != nil {
		t.Fatal(err)
	}

	failedPath := task.TaskStatePath(root, task.WorkflowStateFailed, filepath.Base(matchingPath))
	failed, err := task.Load(failedPath)
	if err != nil {
		t.Fatal(err)
	}
	if failed.Status != task.StatusFailed {
		t.Fatalf("status = %q", failed.Status)
	}
	if failed.Worktree.Path != filepath.Join(root, "worktrees", "matching.yaml") {
		t.Fatalf("worktree changed: %#v", failed.Worktree)
	}
	if len(failed.RevisionRequests) != 1 || failed.RevisionRequests[0].ID != "supervisor-finding-1" {
		t.Fatalf("revision requests changed: %#v", failed.RevisionRequests)
	}
	if len(failed.Attempts) != 2 || failed.Attempts[1].Error == nil {
		t.Fatalf("interruption attempt missing: %#v", failed.Attempts)
	}
	if got := failed.Attempts[1].Error; got.Phase != "daemon" || got.Kind != "daemon_force_stopped" {
		t.Fatalf("interruption error = %#v", got)
	}
	if _, err := os.Stat(queue.OwnerPath(matchingPath)); !os.IsNotExist(err) {
		t.Fatalf("matching owner sidecar remains: %v", err)
	}
	for _, path := range []string{otherPath, recycledPath, missingPath, invalidPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("non-matching task changed: %s: %v", path, err)
		}
	}
}

func TestFailOwnedRunningTasksPreservesUnreadableMatchingTask(t *testing.T) {
	root := t.TempDir()
	if err := queue.EnsureLayout(root); err != nil {
		t.Fatal(err)
	}
	runningPath := task.TaskStatePath(root, task.WorkflowStateRunning, "broken.yaml")
	if err := os.WriteFile(runningPath, []byte("status: ["), 0o600); err != nil {
		t.Fatal(err)
	}
	owner := queue.Owner{PID: 101, ProcessStartedAt: "target-start", RecordedAt: "now"}
	if err := queue.WriteOwner(runningPath, owner); err != nil {
		t.Fatal(err)
	}

	_, err := failOwnedRunningTasks(root, daemonctl.PIDFile{PID: 101, ProcessStartedAt: "target-start"})
	if err == nil {
		t.Fatal("expected task publication error")
	}
	if _, err := os.Stat(runningPath); err != nil {
		t.Fatalf("running task was not preserved: %v", err)
	}
	if _, err := os.Stat(queue.OwnerPath(runningPath)); err != nil {
		t.Fatalf("owner evidence was not preserved: %v", err)
	}
}

func TestFailOwnedRunningTasksPreservesSourceWhenDestinationExists(t *testing.T) {
	root := t.TempDir()
	target := daemonctl.PIDFile{PID: 101, ProcessStartedAt: "target-start"}
	owner := queue.Owner{PID: 101, ProcessStartedAt: "target-start", RecordedAt: "now"}
	runningPath := writeForceStopTask(t, root, "task.yaml", &owner)
	original, err := os.ReadFile(runningPath)
	if err != nil {
		t.Fatal(err)
	}
	failedPath := task.TaskStatePath(root, task.WorkflowStateFailed, filepath.Base(runningPath))
	if err := task.Save(failedPath, task.Task{ID: "existing", Status: task.StatusFailed}); err != nil {
		t.Fatal(err)
	}

	if _, err := failOwnedRunningTasks(root, target); err == nil {
		t.Fatal("expected destination conflict")
	}
	got, err := os.ReadFile(runningPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, original) {
		t.Fatalf("running task changed after failed publication:\n%s", got)
	}
	if _, err := os.Stat(queue.OwnerPath(runningPath)); err != nil {
		t.Fatalf("owner evidence was not preserved: %v", err)
	}
}

func TestFailRunningTaskContinuesAfterOwnerCleanupFailure(t *testing.T) {
	root := t.TempDir()
	runningPath := writeForceStopTask(t, root, "task.yaml", nil)
	ownerPath := queue.OwnerPath(runningPath)
	if err := os.Mkdir(ownerPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ownerPath, "keep"), []byte("owner cleanup blocker"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := failRunningTask(root, runningPath); err != nil {
		t.Fatalf("owner cleanup made task publication fail: %v", err)
	}
	failedPath := task.TaskStatePath(root, task.WorkflowStateFailed, filepath.Base(runningPath))
	failed, err := task.Load(failedPath)
	if err != nil {
		t.Fatal(err)
	}
	if failed.Status != task.StatusFailed {
		t.Fatalf("status = %q", failed.Status)
	}
	if _, err := os.Stat(ownerPath); err != nil {
		t.Fatalf("orphan owner evidence was not retained: %v", err)
	}
}
