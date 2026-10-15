package daemon

import (
	"path/filepath"
	"testing"

	setuppreflight "github.com/shinpr/galley/internal/preflight/setup"
	skeletonpreflight "github.com/shinpr/galley/internal/preflight/skeleton"
	"github.com/shinpr/galley/internal/task"
)

func writeSetupReuseTestResult(t *testing.T, dir string, res *setuppreflight.Result) error {
	t.Helper()
	if err := recordPreflightInputs(dir, "setup", "test", dir); err != nil {
		return err
	}
	return setuppreflight.WriteResult(dir, res)
}

func writeSkeletonReuseTestResult(t *testing.T, dir string, res *skeletonpreflight.Result) error {
	t.Helper()
	if err := recordPreflightInputs(dir, "skeleton", "test", dir); err != nil {
		return err
	}
	return skeletonpreflight.WriteResult(dir, res)
}

func TestReuseCompletedPreflightsFindsEarlierSuccessfulRun(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	taskID := "task-requeue"
	readyRun := filepath.Join(root, "runs", taskID+"-100")
	failedRun := filepath.Join(root, "runs", taskID+"-200")
	currentRun := filepath.Join(root, "runs", taskID+"-300")
	effective := task.Executor{CLI: "codex", Model: "env-model", Effort: "minimal"}

	readySetup := &setuppreflight.Result{Status: setuppreflight.StatusReady, ReadinessEvidence: "ready once"}
	setuppreflight.ApplyExecutorIdentity(readySetup, effective)
	if err := writeSetupReuseTestResult(t, readyRun, readySetup); err != nil {
		t.Fatal(err)
	}
	readySkeleton := &skeletonpreflight.Result{Status: "completed", SourceOfTruth: true, NoSkeletons: []skeletonpreflight.NoOutput{{ACID: "AC1", Reason: "covered elsewhere"}}}
	skeletonpreflight.ApplyExecutorIdentity(readySkeleton, effective)
	if err := writeSkeletonReuseTestResult(t, readyRun, readySkeleton); err != nil {
		t.Fatal(err)
	}
	if err := writeSetupReuseTestResult(t, failedRun, &setuppreflight.Result{Status: setuppreflight.StatusFailed}); err != nil {
		t.Fatal(err)
	}
	if err := writeSkeletonReuseTestResult(t, failedRun, &skeletonpreflight.Result{Status: "failed", SourceOfTruth: true}); err != nil {
		t.Fatal(err)
	}

	setupRes, setupReused, err := reuseReadySetup(preflightReuseQuery{Root: root, TaskID: taskID, RunDir: currentRun, Effective: effective, InputKey: "test"})
	if err != nil || !setupReused || setupRes.ReadinessEvidence != "ready once" {
		t.Fatalf("setup reuse = (%+v, %v, %v)", setupRes, setupReused, err)
	}
	skeletonRes, skeletonReused, err := reuseCompletedAcceptanceSkeleton(preflightReuseQuery{Root: root, TaskID: taskID, RunDir: currentRun, Effective: effective, InputKey: "test", WorkDir: ""})
	if err != nil || !skeletonReused || len(skeletonRes.NoSkeletons) != 1 {
		t.Fatalf("skeleton reuse = (%+v, %v, %v)", skeletonRes, skeletonReused, err)
	}

	if copied, err := setuppreflight.LoadResult(currentRun); err != nil || copied == nil || copied.Status != setuppreflight.StatusReady {
		t.Fatalf("copied setup result = (%+v, %v)", copied, err)
	}
	if copied, err := skeletonpreflight.LoadResult(currentRun); err != nil || copied == nil || copied.Status != "completed" {
		t.Fatalf("copied skeleton result = (%+v, %v)", copied, err)
	}
}

func TestReuseCompletedPreflightsFallsBackWhenNoSuccessExists(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	runDir := filepath.Join(root, "runs", "task-new-100")
	effective := task.Executor{CLI: "claude", Effort: "high"}

	if res, reused, err := reuseReadySetup(preflightReuseQuery{Root: root, TaskID: "task-new", RunDir: runDir, Effective: effective, InputKey: "test"}); err != nil || reused || res != nil {
		t.Fatalf("setup reuse = (%+v, %v, %v)", res, reused, err)
	}
	if res, reused, err := reuseCompletedAcceptanceSkeleton(preflightReuseQuery{Root: root, TaskID: "task-new", RunDir: runDir, Effective: effective, InputKey: "test", WorkDir: ""}); err != nil || reused || res != nil {
		t.Fatalf("skeleton reuse = (%+v, %v, %v)", res, reused, err)
	}
}

func TestReuseCompletedPreflightsRejectsMismatchedExecutorIdentity(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	taskID := "task-identity"
	priorRun := filepath.Join(root, "runs", taskID+"-1")
	currentRun := filepath.Join(root, "runs", taskID+"-2")
	prior := task.Executor{CLI: "codex", Model: "old-model", Effort: "minimal"}
	current := task.Executor{CLI: "codex", Model: "new-model", Effort: "minimal"}

	setupRes := &setuppreflight.Result{Status: setuppreflight.StatusReady, ReadinessEvidence: "stale"}
	setuppreflight.ApplyExecutorIdentity(setupRes, prior)
	if err := writeSetupReuseTestResult(t, priorRun, setupRes); err != nil {
		t.Fatal(err)
	}
	skeletonRes := &skeletonpreflight.Result{Status: "completed", SourceOfTruth: true}
	skeletonpreflight.ApplyExecutorIdentity(skeletonRes, prior)
	if err := writeSkeletonReuseTestResult(t, priorRun, skeletonRes); err != nil {
		t.Fatal(err)
	}

	if res, reused, err := reuseReadySetup(preflightReuseQuery{Root: root, TaskID: taskID, RunDir: currentRun, Effective: current, InputKey: "test"}); err != nil || reused || res != nil {
		t.Fatalf("setup should not reuse mismatched identity: (%+v, %v, %v)", res, reused, err)
	}
	if res, reused, err := reuseCompletedAcceptanceSkeleton(preflightReuseQuery{Root: root, TaskID: taskID, RunDir: currentRun, Effective: current, InputKey: "test", WorkDir: ""}); err != nil || reused || res != nil {
		t.Fatalf("skeleton should not reuse mismatched identity: (%+v, %v, %v)", res, reused, err)
	}
}
