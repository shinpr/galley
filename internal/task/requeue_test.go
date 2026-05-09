package task

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRequeueMovesFailedTaskToQueued(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	failedPath := filepath.Join(root, "tasks", "failed", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(failedPath), 0o755); err != nil {
		t.Fatal(err)
	}
	taskPath := writeTaskYAML(t, "loop_budget: 3")
	loaded, err := Load(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Status = "needs_supervisor_review"
	if err := Save(failedPath, loaded); err != nil {
		t.Fatal(err)
	}

	result, err := Requeue(failedPath, RequeueOptions{
		Reason:              "address review",
		ProcessedCommentIDs: []string{"42"},
		RevisionRequests: []RevisionRequest{{
			ID:        "pr-comment-42",
			Source:    "pr_comment",
			CommentID: "42",
			Text:      "address review",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	queuedPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	if result.To != queuedPath {
		t.Fatalf("to got %q", result.To)
	}
	requeued, err := Load(queuedPath)
	if err != nil {
		t.Fatal(err)
	}
	if requeued.Status != "queued" {
		t.Fatalf("requeued task got status=%q", requeued.Status)
	}
	if requeued.Supervisor.ReviewIterations != 1 {
		t.Fatalf("review iterations got %d", requeued.Supervisor.ReviewIterations)
	}
	if len(requeued.Decisions) != 1 || !strings.Contains(requeued.Decisions[0].Chosen, "address review") {
		t.Fatalf("decisions got %#v", requeued.Decisions)
	}
	if len(requeued.RevisionRequests) != 1 || requeued.RevisionRequests[0].Status != "pending" || requeued.RevisionRequests[0].Text != "address review" {
		t.Fatalf("revision requests got %#v", requeued.RevisionRequests)
	}
	if len(requeued.PR.ProcessedCommentIDs) != 1 || requeued.PR.ProcessedCommentIDs[0] != "42" {
		t.Fatalf("processed comments got %#v", requeued.PR.ProcessedCommentIDs)
	}
	if _, err := os.Stat(failedPath); !os.IsNotExist(err) {
		t.Fatalf("failed path should be moved, err=%v", err)
	}
}

func TestRequeueDoesNotOverwriteQueuedTask(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	failedPath := filepath.Join(root, "tasks", "failed", "task.yaml")
	queuedPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(failedPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(queuedPath), 0o755); err != nil {
		t.Fatal(err)
	}
	taskPath := writeTaskYAML(t, "loop_budget: 3")
	loaded, err := Load(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Status = "failed"
	if err := Save(failedPath, loaded); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(queuedPath, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err = Requeue(failedPath, RequeueOptions{})
	if err == nil {
		t.Fatal("expected destination exists error")
	}
	data, err := os.ReadFile(queuedPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "existing" {
		t.Fatalf("queued task overwritten: %q", string(data))
	}
}
