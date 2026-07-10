package task

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// swapDuplicateScanReader replaces the duplicate-ID scan reader for a single
// test and returns a restore function. Tests using it must not run in parallel
// because the reader is package-global state.
func swapDuplicateScanReader(fn func(string) (string, error)) func() {
	prev := taskIDForDuplicateScan
	taskIDForDuplicateScan = fn
	return func() { taskIDForDuplicateScan = prev }
}

// writeTaskFileWithID writes a valid task file at path with the given ID and status.
func writeTaskFileWithID(t *testing.T, path, id, status string) {
	t.Helper()
	base := writeTaskYAML(t, "loop_budget: 3")
	loaded, err := Load(base)
	if err != nil {
		t.Fatal(err)
	}
	loaded.ID = id
	loaded.Status = status
	if err := Save(path, loaded); err != nil {
		t.Fatal(err)
	}
}

func countTasksWithID(t *testing.T, dir, id string) int {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "*.y*ml"))
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, m := range matches {
		got, err := taskIDFromFile(m)
		if err != nil {
			t.Fatalf("read %s: %v", m, err)
		}
		if got == id {
			count++
		}
	}
	return count
}

// AC1: an existing task relocating queued -> running mid-scan must not abort
// registration of a unique incoming task; a rescan finds it at its new location.
func TestQueueRescansWhenTaskMovesDuringDuplicateScan(t *testing.T) {
	root := t.TempDir()
	queuedDir := filepath.Join(root, "tasks", "queued")
	runningDir := filepath.Join(root, "tasks", "running")
	draftDir := filepath.Join(root, "tasks", "draft")
	for _, d := range []string{queuedDir, runningDir, draftDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	existingQueued := filepath.Join(queuedDir, "existing.yaml")
	writeTaskFileWithID(t, existingQueued, "existing-task", "queued")
	draftPath := filepath.Join(draftDir, "incoming.yaml")
	writeTaskFileWithID(t, draftPath, "incoming-task", "draft")

	moved := false
	restore := swapDuplicateScanReader(func(p string) (string, error) {
		if !moved && filepath.Base(p) == "existing.yaml" && filepath.Base(filepath.Dir(p)) == "queued" {
			moved = true
			if err := os.Rename(existingQueued, filepath.Join(runningDir, "existing.yaml")); err != nil {
				t.Fatal(err)
			}
		}
		return taskIDFromFile(p)
	})
	t.Cleanup(restore)

	result, err := Queue(draftPath, QueueOptions{Root: root})
	if err != nil {
		t.Fatalf("queue should recover from a move race, got error: %v", err)
	}
	if !moved {
		t.Fatal("expected the move-race injection to fire during inspection")
	}
	if result.Task.ID != "incoming-task" || result.Task.Status != "queued" {
		t.Fatalf("queued task got %#v", result.Task)
	}
	if got := countTasksWithID(t, queuedDir, "incoming-task"); got != 1 {
		t.Fatalf("incoming task should appear exactly once in queued, got %d", got)
	}
	if _, err := os.Stat(filepath.Join(runningDir, "existing.yaml")); err != nil {
		t.Fatalf("existing task should remain at its new running location: %v", err)
	}
}

// AC2: a same-ID task relocating mid-scan must still be detected as a duplicate
// at its new location; the rescan must not weaken duplicate protection.
func TestQueueRejectsDuplicateWhenSameIDTaskMovesDuringScan(t *testing.T) {
	root := t.TempDir()
	queuedDir := filepath.Join(root, "tasks", "queued")
	runningDir := filepath.Join(root, "tasks", "running")
	draftDir := filepath.Join(root, "tasks", "draft")
	for _, d := range []string{queuedDir, runningDir, draftDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	existingQueued := filepath.Join(queuedDir, "existing.yaml")
	writeTaskFileWithID(t, existingQueued, "dupe-id", "queued")
	draftPath := filepath.Join(draftDir, "incoming.yaml")
	writeTaskFileWithID(t, draftPath, "dupe-id", "draft")

	moved := false
	restore := swapDuplicateScanReader(func(p string) (string, error) {
		if !moved && filepath.Base(p) == "existing.yaml" && filepath.Base(filepath.Dir(p)) == "queued" {
			moved = true
			if err := os.Rename(existingQueued, filepath.Join(runningDir, "existing.yaml")); err != nil {
				t.Fatal(err)
			}
		}
		return taskIDFromFile(p)
	})
	t.Cleanup(restore)

	_, err := Queue(draftPath, QueueOptions{Root: root})
	if err == nil {
		t.Fatal("expected duplicate task id rejection after rescan")
	}
	if !strings.Contains(err.Error(), `task id "dupe-id" already exists`) {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, statErr := os.Stat(draftPath); statErr != nil {
		t.Fatalf("draft source should remain unchanged after rejection: %v", statErr)
	}
	if got := countTasksWithID(t, queuedDir, "dupe-id"); got != 0 {
		t.Fatalf("no same-ID copy should be published to queued, got %d", got)
	}
}

// AC3: a non-ENOENT inspection error must surface as the underlying cause and
// must not be retried as a move race.
func TestQueueDuplicateScanReportsNonENOENTError(t *testing.T) {
	root := t.TempDir()
	queuedDir := filepath.Join(root, "tasks", "queued")
	draftDir := filepath.Join(root, "tasks", "draft")
	for _, d := range []string{queuedDir, draftDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeTaskFileWithID(t, filepath.Join(queuedDir, "existing.yaml"), "existing-task", "queued")
	draftPath := filepath.Join(draftDir, "incoming.yaml")
	writeTaskFileWithID(t, draftPath, "incoming-task", "draft")

	sentinel := errors.New("permission denied")
	calls := 0
	restore := swapDuplicateScanReader(func(p string) (string, error) {
		if filepath.Base(p) == "existing.yaml" {
			calls++
			return "", sentinel
		}
		return taskIDFromFile(p)
	})
	t.Cleanup(restore)

	_, err := Queue(draftPath, QueueOptions{Root: root})
	if err == nil {
		t.Fatal("expected the underlying inspection error to be reported")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("underlying cause should be preserved, got: %v", err)
	}
	if calls != 1 {
		t.Fatalf("non-ENOENT error must not be retried as a move race, got %d scans", calls)
	}
	if _, statErr := os.Stat(draftPath); statErr != nil {
		t.Fatalf("draft source should remain unchanged: %v", statErr)
	}
}

// AC4: when every scan observes a disappearing path, inspection must fail within
// the bounded retry policy, preserve the source, publish nothing, and report the
// recovery facts distinctly from a task execution failure.
func TestQueueDuplicateScanRetryExhaustion(t *testing.T) {
	root := t.TempDir()
	queuedDir := filepath.Join(root, "tasks", "queued")
	draftDir := filepath.Join(root, "tasks", "draft")
	for _, d := range []string{queuedDir, draftDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeTaskFileWithID(t, filepath.Join(queuedDir, "existing.yaml"), "existing-task", "queued")
	draftPath := filepath.Join(draftDir, "incoming.yaml")
	writeTaskFileWithID(t, draftPath, "incoming-task", "draft")

	calls := 0
	restore := swapDuplicateScanReader(func(p string) (string, error) {
		if filepath.Base(p) == "existing.yaml" {
			calls++
			return "", os.ErrNotExist
		}
		return taskIDFromFile(p)
	})
	t.Cleanup(restore)

	_, err := Queue(draftPath, QueueOptions{Root: root})
	if err == nil {
		t.Fatal("expected retry exhaustion to fail queue registration")
	}
	msg := err.Error()
	for _, want := range []string{"queue registration failed", "preserved", "retry the queue command"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error missing %q recovery fact: %v", want, err)
		}
	}
	if calls != duplicateScanMaxAttempts {
		t.Fatalf("retry must be bounded to %d scans, got %d", duplicateScanMaxAttempts, calls)
	}
	if _, statErr := os.Stat(draftPath); statErr != nil {
		t.Fatalf("draft source must be preserved on retry exhaustion: %v", statErr)
	}
	if got := countTasksWithID(t, queuedDir, "incoming-task"); got != 0 {
		t.Fatalf("no incoming copy should be published on retry exhaustion, got %d", got)
	}
}
