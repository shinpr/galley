package task

import (
	"os"
	"path/filepath"
	"testing"
)

func TestQueueMovesReadyTaskToQueued(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	readyPath := filepath.Join(root, "tasks", "ready", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(readyPath), 0o755); err != nil {
		t.Fatal(err)
	}
	taskPath := writeTaskYAML(t, "loop_budget: 3")
	loaded, err := Load(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Status = "ready"
	if err := Save(readyPath, loaded); err != nil {
		t.Fatal(err)
	}

	result, err := Queue(readyPath, QueueOptions{Reason: "validated by skill"})
	if err != nil {
		t.Fatal(err)
	}
	queuedPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	if result.To != queuedPath {
		t.Fatalf("to got %q", result.To)
	}
	queued, err := Load(queuedPath)
	if err != nil {
		t.Fatal(err)
	}
	if queued.Status != "queued" {
		t.Fatalf("status got %q", queued.Status)
	}
	if len(queued.Attempts) != 1 || queued.Attempts[0].SupervisorVerdict != "queued" {
		t.Fatalf("attempts got %#v", queued.Attempts)
	}
}

func TestQueueMovesDraftTaskToQueued(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	draftPath := filepath.Join(root, "tasks", "draft", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(draftPath), 0o755); err != nil {
		t.Fatal(err)
	}
	path := writeTaskYAML(t, "loop_budget: 3")
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Status = "draft"
	if err := Save(draftPath, loaded); err != nil {
		t.Fatal(err)
	}
	result, err := Queue(draftPath, QueueOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Task.Status != "queued" {
		t.Fatalf("status got %q", result.Task.Status)
	}
}

func TestQueueRejectsNonAuthoringStatuses(t *testing.T) {
	t.Parallel()
	for _, status := range []string{"queued", "running", "needs_supervisor_review", "accepted", "pr_opened", "failed"} {
		status := status
		t.Run(status, func(t *testing.T) {
			t.Parallel()
			path := writeTaskYAML(t, "loop_budget: 3")
			loaded, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			loaded.Status = status
			if err := Save(path, loaded); err != nil {
				t.Fatal(err)
			}
			if _, err := Queue(path, QueueOptions{}); err == nil {
				t.Fatalf("expected %s task error", status)
			}
		})
	}
}
