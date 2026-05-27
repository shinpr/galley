package task

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestArchiveMovesReviewedTask(t *testing.T) {
	root := t.TempDir()
	donePath := filepath.Join(root, "tasks", "done", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(donePath), 0o755); err != nil {
		t.Fatal(err)
	}
	path := writeTaskYAML(t, "loop_budget: 3")
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Status = "accepted"
	if err := Save(donePath, loaded); err != nil {
		t.Fatal(err)
	}

	result, err := Archive(donePath, ArchiveOptions{Reason: "cleanup after review"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Task.Status != "archived" {
		t.Fatalf("status got %q", result.Task.Status)
	}
	archivedPath := filepath.Join(root, "tasks", "archived", "task.yaml")
	if result.To != archivedPath {
		t.Fatalf("to got %q", result.To)
	}
	if _, err := os.Stat(archivedPath); err != nil {
		t.Fatalf("archived task missing: %v", err)
	}
	if _, err := os.Stat(donePath); !os.IsNotExist(err) {
		t.Fatalf("done task should be moved, err=%v", err)
	}
}

func TestArchiveRejectsActiveTask(t *testing.T) {
	path := writeTaskYAML(t, "loop_budget: 3")
	if _, err := Archive(path, ArchiveOptions{}); err == nil {
		t.Fatal("expected active task archive error")
	}
}

func TestArchiveRejectsOpenPRTask(t *testing.T) {
	root := t.TempDir()
	donePath := filepath.Join(root, "tasks", "done", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(donePath), 0o755); err != nil {
		t.Fatal(err)
	}
	path := writeTaskYAML(t, "loop_budget: 3")
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Status = "pr_opened"
	loaded.PR.URL = "https://github.com/example/repo/pull/1"
	loaded.PR.Status = "open"
	if err := Save(donePath, loaded); err != nil {
		t.Fatal(err)
	}

	_, err = Archive(donePath, ArchiveOptions{Reason: "cleanup after review"})
	if err == nil {
		t.Fatal("expected open PR archive error")
	}
	if !strings.Contains(err.Error(), "close or merge the PR before archiving") {
		t.Fatalf("error should explain open PR precondition, got %v", err)
	}
	if _, statErr := os.Stat(donePath); statErr != nil {
		t.Fatalf("open PR task should remain in place: %v", statErr)
	}
}
