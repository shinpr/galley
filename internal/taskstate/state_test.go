package taskstate

import (
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

	if err := Move(root, runningPath, "done", &loaded); err != nil {
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

	err := Move(root, runningPath, "done", nil)
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
	if err := FailMove(root, runningPath, &loaded, nil); err != nil {
		t.Fatal(err)
	}
	moved, err := task.Load(filepath.Join(failedDir, "task.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := moved.Status, "needs_supervisor_review"; got != want {
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
	if err := FailMove(root, runningPath, &loaded, nil); err != nil {
		t.Fatal(err)
	}
	moved, err := task.Load(filepath.Join(failedDir, "task.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := moved.Status, "failed"; got != want {
		t.Fatalf("status got %q, want %q", got, want)
	}
}
