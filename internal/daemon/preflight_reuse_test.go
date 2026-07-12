package daemon

import (
	"path/filepath"
	"testing"

	setuppreflight "github.com/shinpr/galley/internal/preflight/setup"
	skeletonpreflight "github.com/shinpr/galley/internal/preflight/skeleton"
)

func TestReuseCompletedPreflightsFindsEarlierSuccessfulRun(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	taskID := "task-requeue"
	readyRun := filepath.Join(root, "runs", taskID+"-100")
	failedRun := filepath.Join(root, "runs", taskID+"-200")
	currentRun := filepath.Join(root, "runs", taskID+"-300")

	if err := setuppreflight.WriteResult(readyRun, &setuppreflight.Result{Status: setuppreflight.StatusReady, ReadinessEvidence: "ready once"}); err != nil {
		t.Fatal(err)
	}
	if err := skeletonpreflight.WriteResult(readyRun, &skeletonpreflight.Result{Status: "completed", SourceOfTruth: true, NoSkeletons: []skeletonpreflight.NoOutput{{ACID: "AC1", Reason: "covered elsewhere"}}}); err != nil {
		t.Fatal(err)
	}
	if err := setuppreflight.WriteResult(failedRun, &setuppreflight.Result{Status: setuppreflight.StatusFailed}); err != nil {
		t.Fatal(err)
	}
	if err := skeletonpreflight.WriteResult(failedRun, &skeletonpreflight.Result{Status: "failed", SourceOfTruth: true}); err != nil {
		t.Fatal(err)
	}

	setupRes, setupReused, err := reuseReadySetup(root, taskID, currentRun)
	if err != nil || !setupReused || setupRes.ReadinessEvidence != "ready once" {
		t.Fatalf("setup reuse = (%+v, %v, %v)", setupRes, setupReused, err)
	}
	skeletonRes, skeletonReused, err := reuseCompletedAcceptanceSkeleton(root, taskID, currentRun)
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

	if res, reused, err := reuseReadySetup(root, "task-new", runDir); err != nil || reused || res != nil {
		t.Fatalf("setup reuse = (%+v, %v, %v)", res, reused, err)
	}
	if res, reused, err := reuseCompletedAcceptanceSkeleton(root, "task-new", runDir); err != nil || reused || res != nil {
		t.Fatalf("skeleton reuse = (%+v, %v, %v)", res, reused, err)
	}
}
