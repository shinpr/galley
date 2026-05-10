package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shinpr/galley/internal/supervisor"
	"github.com/shinpr/galley/internal/task"
)

// TestAcceptanceSkeletonLifecycleDowngradesWhenLatestAttemptHasNoCheckpoint is
// the regression for attempt-scoped skeleton checkpoint evidence: attempt-1
// recorded a passing checkpoint, but attempt-2 (the latest attempt — e.g. the
// executor hard-stopped before result.Complete ran) produced no
// skeleton_checkpoint_results.json. The acceptance gate must treat the latest
// attempt as missing evidence and downgrade the accepted verdict to
// needs_supervisor_review rather than reusing attempt-1's stale pass.
func TestAcceptanceSkeletonLifecycleDowngradesWhenLatestAttemptHasNoCheckpoint(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	runningDir := filepath.Join(root, "tasks", "running")
	if err := os.MkdirAll(runningDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runningPath := filepath.Join(runningDir, "task.yaml")
	loaded := acceptanceGateTask(true)
	loaded.Status = "running"
	loaded.Attempts = []task.Attempt{{Number: 1}, {Number: 2}}
	if err := task.Save(runningPath, *loaded); err != nil {
		t.Fatal(err)
	}

	// attempt-1 has a passing skeleton checkpoint recorded.
	runDir := writeAcceptanceGateRun(t, "completed",
		[]AcceptanceSkeletonOutput{skeletonOutput()},
		[]CheckpointResult{{ACID: "AC1", Command: "go test ./internal/foo/", Status: "passed", Source: "acceptance_skeleton"}},
	)
	// attempt-2 is the latest attempt but produced no checkpoint file.
	if err := os.MkdirAll(filepath.Join(runDir, "attempt-2"), 0o700); err != nil {
		t.Fatal(err)
	}

	// LoadLatestCheckpointResults must inspect only attempt-2 and report
	// missing evidence — no fallback to attempt-1.
	results, latestDir, err := LoadLatestCheckpointResults(runDir)
	if err != nil {
		t.Fatalf("LoadLatestCheckpointResults: %v", err)
	}
	if results != nil {
		t.Fatalf("expected nil checkpoint results for latest attempt, got %#v", results)
	}
	if filepath.Base(latestDir) != "attempt-2" {
		t.Fatalf("latest attempt dir = %q; want attempt-2", latestDir)
	}

	nextPrompt, done, err := applySupervisorVerdict(context.Background(), context.Background(), verdictApplication{
		Opts:        Options{Root: root}.withDefaults(),
		RunningPath: runningPath,
		Loaded:      loaded,
		RunDir:      runDir,
		Attempt:     2,
		Verdict:     supervisor.Verdict{Status: "accepted", Summary: "looks good"},
	})
	if err != nil {
		t.Fatalf("applySupervisorVerdict: %v", err)
	}
	if !done || nextPrompt != "" {
		t.Fatalf("done=%v nextPrompt=%q", done, nextPrompt)
	}
	if loaded.Status != "needs_supervisor_review" {
		t.Fatalf("status = %q; want needs_supervisor_review", loaded.Status)
	}
	if _, statErr := os.Stat(filepath.Join(root, "tasks", "failed", "task.yaml")); statErr != nil {
		t.Fatalf("downgraded task not in tasks/failed: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(root, "tasks", "done", "task.yaml")); statErr == nil {
		t.Fatal("accepted-then-downgraded task must not reach tasks/done")
	}
	saved, err := task.Load(filepath.Join(root, "tasks", "failed", "task.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Risks) == 0 {
		t.Fatal("expected an acceptance-gate risk to be recorded")
	}
	last := saved.Risks[len(saved.Risks)-1].Detail
	if !strings.Contains(last, "acceptance skeleton gate") || !strings.Contains(last, "missing or failed") {
		t.Fatalf("acceptance-gate risk missing checkpoint detail: %q", last)
	}
}

// TestAcceptanceSkeletonLifecycleUsesLatestAttemptCheckpointEvidence proves the
// gate reads the latest attempt's evidence rather than the first: attempt-1
// recorded a failing checkpoint, but attempt-2 (the latest) recorded a passing
// one, so acceptance is allowed.
func TestAcceptanceSkeletonLifecycleUsesLatestAttemptCheckpointEvidence(t *testing.T) {
	t.Parallel()
	runDir := writeAcceptanceGateRun(t, "completed",
		[]AcceptanceSkeletonOutput{skeletonOutput()},
		[]CheckpointResult{{ACID: "AC1", Command: "go test ./internal/foo/", Status: "failed", ExitCode: 1, Source: "acceptance_skeleton"}},
	)
	if err := WriteCheckpointResults(filepath.Join(runDir, "attempt-2"),
		[]CheckpointResult{{ACID: "AC1", Command: "go test ./internal/foo/", Status: "passed", Source: "acceptance_skeleton"}}); err != nil {
		t.Fatal(err)
	}

	results, latestDir, err := LoadLatestCheckpointResults(runDir)
	if err != nil {
		t.Fatalf("LoadLatestCheckpointResults: %v", err)
	}
	if filepath.Base(latestDir) != "attempt-2" {
		t.Fatalf("latest attempt dir = %q; want attempt-2", latestDir)
	}
	if len(results) != 1 || results[0].Status != "passed" {
		t.Fatalf("expected attempt-2 passing checkpoint, got %#v", results)
	}

	if reason, ok := evaluateAcceptanceGate(acceptanceGateTask(true), runDir); !ok {
		t.Fatalf("expected gate to allow acceptance using latest attempt evidence, reason=%q", reason)
	}
}
