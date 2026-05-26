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

func TestRequeuePreservesPRAuthorLogin(t *testing.T) {
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
	loaded.PR.URL = "https://github.com/example/galley/pull/1"
	loaded.PR.Status = "open"
	loaded.PR.AuthorLogin = "pr-author"
	if err := Save(failedPath, loaded); err != nil {
		t.Fatal(err)
	}

	result, err := Requeue(failedPath, RequeueOptions{Reason: "rerun"})
	if err != nil {
		t.Fatal(err)
	}
	requeued, err := Load(result.To)
	if err != nil {
		t.Fatal(err)
	}
	if requeued.PR.AuthorLogin != "pr-author" {
		t.Fatalf("PR.AuthorLogin not preserved across requeue: %q", requeued.PR.AuthorLogin)
	}
}

func TestLoadAcceptsTaskWithoutPRAuthorLogin(t *testing.T) {
	t.Parallel()
	// Older task YAML written before PR.AuthorLogin existed must still
	// decode cleanly under strict KnownFields parsing.
	taskPath := writeTaskYAML(t, "loop_budget: 3")
	loaded, err := Load(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.PR.AuthorLogin != "" {
		t.Fatalf("expected empty PR.AuthorLogin for task without author_login, got %q", loaded.PR.AuthorLogin)
	}
}

func TestSaveAndLoadRoundTripsPRAuthorLogin(t *testing.T) {
	t.Parallel()
	taskPath := writeTaskYAML(t, "loop_budget: 3")
	loaded, err := Load(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	loaded.PR.URL = "https://github.com/example/galley/pull/2"
	loaded.PR.Status = "open"
	loaded.PR.AuthorLogin = "task-author"
	if err := Save(taskPath, loaded); err != nil {
		t.Fatal(err)
	}
	roundTripped, err := Load(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	if roundTripped.PR.AuthorLogin != "task-author" {
		t.Fatalf("PR.AuthorLogin did not round-trip: %q", roundTripped.PR.AuthorLogin)
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

func TestRequeueCopiesExternalReviewedTaskIntoRoot(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	external := writeTaskYAML(t, "loop_budget: 3")
	loaded, err := Load(external)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Status = "failed"
	if err := Save(external, loaded); err != nil {
		t.Fatal(err)
	}

	result, err := Requeue(external, RequeueOptions{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	queuedPath := filepath.Join(root, "tasks", "queued", filepath.Base(external))
	if result.To != queuedPath {
		t.Fatalf("to got %q", result.To)
	}
	if _, err := os.Stat(external); err != nil {
		t.Fatalf("external source should remain: %v", err)
	}
	requeued, err := Load(queuedPath)
	if err != nil {
		t.Fatal(err)
	}
	if requeued.Status != "queued" {
		t.Fatalf("requeued status got %q", requeued.Status)
	}
}

func TestRequeueAssignsUniqueRevisionRequestIDs(t *testing.T) {
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
	loaded.Status = "failed"
	loaded.RevisionRequests = []RevisionRequest{{ID: "revision-2", Text: "existing"}}
	if err := Save(failedPath, loaded); err != nil {
		t.Fatal(err)
	}

	result, err := Requeue(failedPath, RequeueOptions{RevisionRequests: []RevisionRequest{
		{Text: "first"},
		{Text: "second"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(result.Task.RevisionRequests), 3; got != want {
		t.Fatalf("revision request count got %d, want %d: %#v", got, want, result.Task.RevisionRequests)
	}
	if got, want := result.Task.RevisionRequests[1].ID, "revision-3"; got != want {
		t.Fatalf("first generated ID got %q, want %q", got, want)
	}
	if got, want := result.Task.RevisionRequests[2].ID, "revision-4"; got != want {
		t.Fatalf("second generated ID got %q, want %q", got, want)
	}
}
