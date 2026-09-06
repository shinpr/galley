package queue

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shinpr/galley/internal/task"
)

func TestClaimTaskDoesNotOverwriteRunningTask(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	queuedDir := filepath.Join(root, "tasks", "queued")
	runningDir := filepath.Join(root, "tasks", "running")
	if err := os.MkdirAll(queuedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(runningDir, 0o755); err != nil {
		t.Fatal(err)
	}
	queuedPath := filepath.Join(queuedDir, "task.yaml")
	runningPath := filepath.Join(runningDir, "task.yaml")
	if err := os.WriteFile(queuedPath, []byte("queued"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runningPath, []byte("running"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := ClaimTask(root, queuedPath)
	if !errors.Is(err, ErrClaimConflict) {
		t.Fatalf("expected claim conflict, got %v", err)
	}
	data, err := os.ReadFile(runningPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "running" {
		t.Fatalf("running task was overwritten: %q", string(data))
	}
	if _, err := os.Stat(runningPath + ".lock"); !os.IsNotExist(err) {
		t.Fatalf("lock should be cleaned up, err=%v", err)
	}
	if _, err := os.Stat(queuedPath + ".lock"); !os.IsNotExist(err) {
		t.Fatalf("source lock should be cleaned up, err=%v", err)
	}
}

func TestClaimTaskHonorsExistingSourceLock(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	if err := EnsureLayout(root); err != nil {
		t.Fatal(err)
	}
	queuedPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	if err := os.WriteFile(queuedPath, []byte("queued"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(queuedPath+".lock", []byte("lock"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := ClaimTask(root, queuedPath)
	if !errors.Is(err, ErrClaimConflict) {
		t.Fatalf("expected claim conflict, got %v", err)
	}
	if _, err := os.Stat(queuedPath); err != nil {
		t.Fatalf("queued task should remain: %v", err)
	}
}

func TestQueuedTasksMatchesYmlExtension(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	if err := EnsureLayout(root); err != nil {
		t.Fatal(err)
	}
	ymlPath := filepath.Join(root, "tasks", "queued", "task.yml")
	if err := task.Save(ymlPath, task.Task{ID: "task", Status: "queued", Scope: task.Scope{CWD: t.TempDir()}}); err != nil {
		t.Fatal(err)
	}
	matches, err := QueuedTasks(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0] != ymlPath {
		t.Fatalf("expected .yml task to be queued, got %v", matches)
	}
}

func TestRunningRepoCountsSkipsCorruptFileAndCountsYml(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	if err := EnsureLayout(root); err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	if err := task.Save(filepath.Join(root, "tasks", "running", "good.yml"), task.Task{
		ID: "good", Status: "running", Scope: task.Scope{CWD: repo},
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "tasks", "running", "corrupt.yaml"), []byte("::not yaml::"), 0o600); err != nil {
		t.Fatal(err)
	}
	counts, err := RunningRepoCounts(root)
	if err != nil {
		t.Fatalf("one corrupt running file must not fail scheduling: %v", err)
	}
	if got := counts[RepoConcurrencyKey(repo)]; got != 1 {
		t.Fatalf("expected .yml running task counted once, got %d (%v)", got, counts)
	}
}

func TestRecoverStaleClaimsRequeuesRunningTaskAndRemovesLock(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	if err := EnsureLayout(root); err != nil {
		t.Fatal(err)
	}
	runningPath := filepath.Join(root, "tasks", "running", "task.yaml")
	if err := task.Save(runningPath, task.Task{
		ID:     "task",
		Status: "running",
		Scope:  task.Scope{CWD: t.TempDir()},
	}); err != nil {
		t.Fatal(err)
	}
	lockPath := runningPath + ".lock"
	if err := os.WriteFile(lockPath, []byte("lock"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(runningPath, old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(lockPath, old, old); err != nil {
		t.Fatal(err)
	}

	if err := RecoverStaleClaims(root, time.Hour, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Fatalf("expected stale lock removed, err=%v", err)
	}
	queuedPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	requeued, err := task.Load(queuedPath)
	if err != nil {
		t.Fatal(err)
	}
	if requeued.Status != "queued" {
		t.Fatalf("status got %q", requeued.Status)
	}
	if _, err := os.Stat(runningPath); !os.IsNotExist(err) {
		t.Fatalf("expected running task moved, err=%v", err)
	}
}

func TestRecoverStaleClaimsIgnoresQueuedDestinationConflict(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	if err := EnsureLayout(root); err != nil {
		t.Fatal(err)
	}
	runningPath := filepath.Join(root, "tasks", "running", "task.yaml")
	if err := task.Save(runningPath, task.Task{
		ID:     "running",
		Status: "running",
		Scope:  task.Scope{CWD: t.TempDir()},
	}); err != nil {
		t.Fatal(err)
	}
	queuedPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	if err := task.Save(queuedPath, task.Task{
		ID:     "queued",
		Status: "queued",
		Scope:  task.Scope{CWD: t.TempDir()},
	}); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(runningPath, old, old); err != nil {
		t.Fatal(err)
	}

	if err := RecoverStaleClaims(root, time.Hour, time.Now()); err != nil {
		t.Fatal(err)
	}
	running, err := task.Load(runningPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := running.Status, task.Status("queued"); got != want {
		t.Fatalf("running status got %q, want %q", got, want)
	}
	queued, err := task.Load(queuedPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := queued.ID, "queued"; got != want {
		t.Fatalf("queued task ID got %q, want %q", got, want)
	}
}

func TestRecoverStaleClaimsRemovesQueuedSourceLock(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	if err := EnsureLayout(root); err != nil {
		t.Fatal(err)
	}
	queuedPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	if err := os.WriteFile(queuedPath, []byte("queued"), 0o600); err != nil {
		t.Fatal(err)
	}
	lockPath := queuedPath + ".lock"
	if err := os.WriteFile(lockPath, []byte("lock"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(lockPath, old, old); err != nil {
		t.Fatal(err)
	}

	if err := RecoverStaleClaims(root, time.Hour, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Fatalf("expected stale queued lock removed, err=%v", err)
	}
}
