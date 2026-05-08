package task

import (
	"os"
	"path/filepath"
	"testing"
)

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
	if len(result.Task.Attempts) != 1 || result.Task.Attempts[0].SupervisorVerdict != "queued" {
		t.Fatalf("attempts got %#v", result.Task.Attempts)
	}
}

func TestQueueRejectsNonAuthoringStatuses(t *testing.T) {
	t.Parallel()
	for _, status := range []string{"ready", "queued", "running", "needs_supervisor_review", "accepted", "pr_opened", "failed"} {
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
