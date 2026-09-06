package taskstate

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/shinpr/galley/internal/task"
)

func TestMoveUpdatedTaskDoesNotLeaveRunningCopy(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	runningDir := filepath.Join(root, "tasks", "running")
	doneDir := filepath.Join(root, "tasks", "done")
	if err := os.MkdirAll(runningDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(doneDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runningPath := filepath.Join(runningDir, "task.yaml")
	loaded := task.Task{ID: "task", Status: "running"}
	if err := task.Save(runningPath, loaded); err != nil {
		t.Fatal(err)
	}
	loaded.Status = "accepted"

	if err := MoveToStatus(root, runningPath, &loaded); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(runningPath); !os.IsNotExist(err) {
		t.Fatalf("running task should be gone, err=%v", err)
	}
	moved, err := task.Load(filepath.Join(doneDir, "task.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if moved.Status != "accepted" {
		t.Fatalf("status got %q", moved.Status)
	}
}

func TestMoveDoesNotOverwriteDestination(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	runningDir := filepath.Join(root, "tasks", "running")
	doneDir := filepath.Join(root, "tasks", "done")
	if err := os.MkdirAll(runningDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(doneDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runningPath := filepath.Join(runningDir, "task.yaml")
	donePath := filepath.Join(doneDir, "task.yaml")
	if err := task.Save(runningPath, task.Task{ID: "new", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if err := task.Save(donePath, task.Task{ID: "existing", Status: "accepted"}); err != nil {
		t.Fatal(err)
	}

	updated := task.Task{ID: "new", Status: task.StatusAccepted}
	err := MoveToStatus(root, runningPath, &updated)
	if err == nil {
		t.Fatal("expected overwrite error")
	}
	existing, err := task.Load(donePath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := existing.ID, "existing"; got != want {
		t.Fatalf("destination ID got %q, want %q", got, want)
	}
}

func TestFailMovePreservesReviewStatus(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	runningDir := filepath.Join(root, "tasks", "running")
	failedDir := filepath.Join(root, "tasks", "failed")
	if err := os.MkdirAll(runningDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(failedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runningPath := filepath.Join(runningDir, "task.yaml")
	loaded := task.Task{ID: "task", Status: "needs_supervisor_review"}
	if err := task.Save(runningPath, loaded); err != nil {
		t.Fatal(err)
	}
	if err := FailMoveToStatus(root, runningPath, &loaded, nil); err != nil {
		t.Fatal(err)
	}
	moved, err := task.Load(filepath.Join(failedDir, "task.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := moved.Status, task.Status("needs_supervisor_review"); got != want {
		t.Fatalf("status got %q, want %q", got, want)
	}
}

func TestFailMoveDefaultsRunningStatusToFailed(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	runningDir := filepath.Join(root, "tasks", "running")
	failedDir := filepath.Join(root, "tasks", "failed")
	if err := os.MkdirAll(runningDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(failedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runningPath := filepath.Join(runningDir, "task.yaml")
	loaded := task.Task{ID: "task", Status: "running"}
	if err := task.Save(runningPath, loaded); err != nil {
		t.Fatal(err)
	}
	if err := FailMoveToStatus(root, runningPath, &loaded, nil); err != nil {
		t.Fatal(err)
	}
	moved, err := task.Load(filepath.Join(failedDir, "task.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := moved.Status, task.Status("failed"); got != want {
		t.Fatalf("status got %q, want %q", got, want)
	}
}

func TestRecoverUnreadableClaimToFailedPreservesBytes(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	runningDir := task.TaskStateDir(root, task.WorkflowStateRunning)
	if err := os.MkdirAll(runningDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runningPath := filepath.Join(runningDir, "task.yaml")
	want := []byte("invalid: [yaml")
	if err := os.WriteFile(runningPath, want, 0o600); err != nil {
		t.Fatal(err)
	}
	primary := errors.New("decode failed")
	if err := RecoverUnreadableClaimToFailed(root, runningPath, primary); !errors.Is(err, primary) {
		t.Fatalf("error = %v; want primary", err)
	}
	got, err := os.ReadFile(task.TaskStatePath(root, task.WorkflowStateFailed, "task.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("bytes changed: %q", got)
	}
}

func TestFailMoveToStatusRejectsNilTaskWithoutMoving(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	runningDir := task.TaskStateDir(root, task.WorkflowStateRunning)
	if err := os.MkdirAll(runningDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runningPath := filepath.Join(runningDir, "task.yaml")
	if err := os.WriteFile(runningPath, []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := FailMoveToStatus(root, runningPath, nil, errors.New("primary")); err == nil {
		t.Fatal("expected nil task error")
	}
	if _, err := os.Stat(runningPath); err != nil {
		t.Fatalf("source moved: %v", err)
	}
}

func TestMoveToStatusRejectsUnknownStatusWithoutChangingSource(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	runningDir := task.TaskStateDir(root, task.WorkflowStateRunning)
	if err := os.MkdirAll(runningDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runningPath := filepath.Join(runningDir, "task.yaml")
	original := task.Task{ID: "task", Status: task.StatusRunning}
	if err := task.Save(runningPath, original); err != nil {
		t.Fatal(err)
	}
	updated := original
	updated.Status = "unknown"
	if err := MoveToStatus(root, runningPath, &updated); err == nil {
		t.Fatal("expected unknown status error")
	}
	got, err := task.Load(runningPath)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != task.StatusRunning {
		t.Fatalf("source status changed to %q", got.Status)
	}
}

func TestRecoverUnreadableClaimToFailedPreservesPrimaryAndMoveErrors(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	runningDir := task.TaskStateDir(root, task.WorkflowStateRunning)
	failedDir := task.TaskStateDir(root, task.WorkflowStateFailed)
	if err := os.MkdirAll(runningDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(failedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runningPath := filepath.Join(runningDir, "task.yaml")
	if err := os.WriteFile(runningPath, []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runningPath+".lock", []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	primary := errors.New("decode failed")
	err := RecoverUnreadableClaimToFailed(root, runningPath, primary)
	if !errors.Is(err, primary) || !errors.Is(err, os.ErrExist) {
		t.Fatalf("error = %v; want primary and source lock conflict", err)
	}
	if _, err := os.Stat(runningPath); err != nil {
		t.Fatalf("source lost: %v", err)
	}
}
