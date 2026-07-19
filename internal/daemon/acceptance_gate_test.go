package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	skeletonpreflight "github.com/shinpr/galley/internal/preflight/skeleton"
	"github.com/shinpr/galley/internal/supervisor"
	"github.com/shinpr/galley/internal/task"
)

func acceptanceGateTask() *task.Task {
	return &task.Task{
		ID:                 "acceptance-gate-test",
		AcceptanceCriteria: []task.AcceptanceCriterion{{ID: "AC1", Text: "x", Status: "pending"}},
		Preflight: &task.Preflight{AcceptanceSkeleton: &task.AcceptanceSkeletonConfig{
			Enabled: true,
		}},
	}
}

// writeAcceptanceGateRun seeds runs/<run-id>/preflight_result.json so
// evaluateAcceptanceGate reads the same preflight evidence shape the daemon
// writes at runtime.
func writeAcceptanceGateRun(t *testing.T, status string, outputs []skeletonpreflight.Output) string {
	t.Helper()
	runDir := t.TempDir()
	res := &skeletonpreflight.Result{
		Status:        status,
		SourceOfTruth: true,
		Outputs:       outputs,
		Baseline:      skeletonpreflight.Baseline{SkeletonHashes: []skeletonpreflight.SkeletonHash{}},
	}
	if err := skeletonpreflight.WriteResult(runDir, res); err != nil {
		t.Fatal(err)
	}
	return runDir
}

func skeletonOutput() skeletonpreflight.Output {
	return skeletonpreflight.Output{
		ACID:                   "AC1",
		Path:                   "internal/foo/foo_test.go",
		Kind:                   "go-test",
		Purpose:                "verify AC1",
		ImplementationRequired: true,
	}
}

func TestAcceptanceGateRejectsMissingRequiredSkeletonCoverage(t *testing.T) {
	t.Parallel()
	runDir := writeAcceptanceGateRun(t, "completed", nil)
	reason, ok := evaluateAcceptanceGate(acceptanceGateTask(), runDir)
	if ok {
		t.Fatalf("expected missing skeleton coverage to block acceptance, reason=%q", reason)
	}
	if !strings.Contains(reason, "no skeleton output") {
		t.Fatalf("reason missing detail: %q", reason)
	}
}

func TestAcceptanceGateAllowsWhenRequiredSkeletonCoverageExists(t *testing.T) {
	t.Parallel()
	runDir := writeAcceptanceGateRun(t, "completed", []skeletonpreflight.Output{skeletonOutput()})
	reason, ok := evaluateAcceptanceGate(acceptanceGateTask(), runDir)
	if !ok {
		t.Fatalf("expected gate to allow acceptance, reason=%q", reason)
	}
}

func TestAcceptanceGateRejectsFailedPreflightResult(t *testing.T) {
	t.Parallel()
	runDir := t.TempDir()
	res := &skeletonpreflight.Result{
		Status:        "failed",
		SourceOfTruth: true,
		Baseline:      skeletonpreflight.Baseline{SkeletonHashes: []skeletonpreflight.SkeletonHash{}},
		Error:         &skeletonpreflight.PreflightError{Phase: "acceptance_skeleton_creator", Message: "creator command exited 1"},
	}
	if err := skeletonpreflight.WriteResult(runDir, res); err != nil {
		t.Fatal(err)
	}
	reason, ok := evaluateAcceptanceGate(acceptanceGateTask(), runDir)
	if ok || !strings.Contains(reason, "preflight failed") {
		t.Fatalf("ok=%v reason=%q", ok, reason)
	}
}

// TestAcceptanceGateLifecycleRejectsAcceptedVerdictBeforeFinalize proves the
// daemon records a failed task and never reaches the "done" state when required
// skeleton coverage is missing.
func TestAcceptanceGateLifecycleRejectsAcceptedVerdictBeforeFinalize(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	runningDir := filepath.Join(root, "tasks", "running")
	if err := os.MkdirAll(runningDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runningPath := filepath.Join(runningDir, "task.yaml")
	loaded := acceptanceGateTask()
	loaded.Status = "running"
	loaded.Attempts = []task.Attempt{{Number: 1}}
	if err := task.Save(runningPath, *loaded); err != nil {
		t.Fatal(err)
	}
	runDir := writeAcceptanceGateRun(t, "completed", nil)

	done, err := applySupervisorVerdict(context.Background(), context.Background(), verdictApplication{
		Opts:        Options{Root: root}.withDefaults(),
		RunningPath: runningPath,
		Loaded:      loaded,
		RunDir:      runDir,
		Attempt:     1,
		Verdict:     supervisor.Verdict{Status: "accepted", Summary: "looks good"},
	})
	if err != nil {
		t.Fatalf("applySupervisorVerdict: %v", err)
	}
	if !done {
		t.Fatal("accepted verdict rejection must end the run")
	}
	if loaded.Status != "failed" {
		t.Fatalf("status = %q; want failed", loaded.Status)
	}
	// The downgrade must move the task to tasks/failed (not tasks/done).
	if _, statErr := os.Stat(filepath.Join(root, "tasks", "failed", "task.yaml")); statErr != nil {
		t.Fatalf("downgraded task not in tasks/failed: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(root, "tasks", "done", "task.yaml")); statErr == nil {
		t.Fatal("rejected task must not reach tasks/done")
	}
	saved, err := task.Load(filepath.Join(root, "tasks", "failed", "task.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Risks) == 0 || !strings.Contains(saved.Risks[len(saved.Risks)-1].Detail, "acceptance skeleton gate") {
		t.Fatalf("expected acceptance-gate risk, got %#v", saved.Risks)
	}
}
