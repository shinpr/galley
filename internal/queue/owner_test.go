package queue

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shinpr/galley/internal/task"
)

func writeRunningTask(t *testing.T, root, name string) string {
	t.Helper()
	runningPath := filepath.Join(root, "tasks", "running", name)
	if err := task.Save(runningPath, task.Task{ID: name, Status: "running", Scope: task.Scope{CWD: t.TempDir()}}); err != nil {
		t.Fatal(err)
	}
	return runningPath
}

func TestRecoverInterruptedRunningRequeuesDeadOwnerWithoutTTL(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	if err := EnsureLayout(root); err != nil {
		t.Fatal(err)
	}
	runningPath := writeRunningTask(t, root, "interrupted.yaml")
	if err := WriteOwner(runningPath, Owner{PID: 424242, RecordedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	// Fresh mtime: TTL-based recovery would not act, but the owner is dead.
	deadOwner := func(Owner) (bool, error) { return false, nil }
	if err := RecoverInterruptedRunning(root, deadOwner); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(runningPath); !os.IsNotExist(err) {
		t.Fatalf("expected running task moved, err=%v", err)
	}
	queuedPath := filepath.Join(root, "tasks", "queued", "interrupted.yaml")
	requeued, err := task.Load(queuedPath)
	if err != nil {
		t.Fatal(err)
	}
	if requeued.Status != "queued" {
		t.Fatalf("status got %q", requeued.Status)
	}
	if _, err := os.Stat(OwnerPath(runningPath)); !os.IsNotExist(err) {
		t.Fatalf("expected owner sidecar removed, err=%v", err)
	}
}

func TestRecoverInterruptedRunningPreservesLiveOwner(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	if err := EnsureLayout(root); err != nil {
		t.Fatal(err)
	}
	runningPath := writeRunningTask(t, root, "owned.yaml")
	if err := WriteOwner(runningPath, Owner{PID: os.Getpid(), RecordedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	liveOwner := func(Owner) (bool, error) { return true, nil }
	if err := RecoverInterruptedRunning(root, liveOwner); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(runningPath); err != nil {
		t.Fatalf("live-owned running task must be preserved: %v", err)
	}
	if _, err := os.Stat(OwnerPath(runningPath)); err != nil {
		t.Fatalf("owner sidecar must be preserved: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "tasks", "queued", "owned.yaml")); !os.IsNotExist(err) {
		t.Fatalf("live-owned task must not be requeued, err=%v", err)
	}
}

func TestRecoverInterruptedRunningLeavesFreshTaskWithoutOwnerSidecar(t *testing.T) {
	// Regression: a running task with a fresh mtime and no owner sidecar must not
	// be requeued immediately on startup. It could be a claim whose owner sidecar
	// has not been written yet, live work from a concurrently running daemon, or a
	// task claimed by an older Galley. Recovery is left to the mtime-based
	// ClaimTTL backstop, which only acts once the claim is genuinely stale.
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	if err := EnsureLayout(root); err != nil {
		t.Fatal(err)
	}
	runningPath := writeRunningTask(t, root, "fresh.yaml")
	called := false
	if err := RecoverInterruptedRunning(root, func(Owner) (bool, error) { called = true; return true, nil }); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("ownerLive should not be consulted when no owner sidecar exists")
	}
	if _, err := os.Stat(runningPath); err != nil {
		t.Fatalf("fresh running task without owner sidecar must be preserved: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "tasks", "queued", "fresh.yaml")); !os.IsNotExist(err) {
		t.Fatalf("fresh running task without owner sidecar must not be requeued, err=%v", err)
	}
}

func TestRecoverInterruptedRunningLeavesTaskWithInvalidOwnerSidecar(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	if err := EnsureLayout(root); err != nil {
		t.Fatal(err)
	}
	runningPath := writeRunningTask(t, root, "corrupt.yaml")
	if err := os.WriteFile(OwnerPath(runningPath), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	called := false
	if err := RecoverInterruptedRunning(root, func(Owner) (bool, error) { called = true; return false, nil }); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("ownerLive should not be consulted for an invalid owner sidecar")
	}
	if _, err := os.Stat(runningPath); err != nil {
		t.Fatalf("running task with invalid owner sidecar must be preserved: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "tasks", "queued", "corrupt.yaml")); !os.IsNotExist(err) {
		t.Fatalf("running task with invalid owner sidecar must not be requeued, err=%v", err)
	}
}

func TestRecoverInterruptedRunningRequeuesByTTLAfterStale(t *testing.T) {
	// A running task with no owner metadata is still recovered once its claim
	// ages past ClaimTTL, so interrupted work from an older Galley is not lost.
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	if err := EnsureLayout(root); err != nil {
		t.Fatal(err)
	}
	runningPath := writeRunningTask(t, root, "ownerless.yaml")
	if err := RecoverInterruptedRunning(root, func(Owner) (bool, error) { return false, nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(runningPath); err != nil {
		t.Fatalf("ownerless task must remain until ClaimTTL: %v", err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(runningPath, old, old); err != nil {
		t.Fatal(err)
	}
	if err := RecoverStaleClaims(root, time.Hour, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "tasks", "queued", "ownerless.yaml")); err != nil {
		t.Fatalf("expected stale ownerless running task requeued by ClaimTTL: %v", err)
	}
	if _, err := os.Stat(runningPath); !os.IsNotExist(err) {
		t.Fatalf("expected running task moved, err=%v", err)
	}
}

func TestRecoverInterruptedRunningRemovesOrphanOwnerSidecars(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	if err := EnsureLayout(root); err != nil {
		t.Fatal(err)
	}
	orphan := filepath.Join(root, "tasks", "running", "gone.yaml.owner")
	if err := os.WriteFile(orphan, []byte(`{"pid":1,"recorded_at":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RecoverInterruptedRunning(root, func(Owner) (bool, error) { return false, nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("expected orphan owner sidecar removed, err=%v", err)
	}
}

func TestRecoverStaleClaimsStillRequeuesByTTL(t *testing.T) {
	// Compatibility: a stale running task with no owner metadata is still recovered
	// by the existing mtime-based ClaimTTL path.
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	if err := EnsureLayout(root); err != nil {
		t.Fatal(err)
	}
	runningPath := writeRunningTask(t, root, "stale.yaml")
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(runningPath, old, old); err != nil {
		t.Fatal(err)
	}
	if err := RecoverStaleClaims(root, time.Hour, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "tasks", "queued", "stale.yaml")); err != nil {
		t.Fatalf("expected TTL-based requeue: %v", err)
	}
}
