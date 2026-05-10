package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shinpr/galley/internal/profile"
	"github.com/shinpr/galley/internal/runner"
	"github.com/shinpr/galley/internal/supervisor"
	"github.com/shinpr/galley/internal/task"
)

func acceptanceGateBoolPtr(b bool) *bool { return &b }

// acceptanceGateTask builds a minimal task with the acceptance skeleton stage
// enabled so evaluateAcceptanceGate exercises the skeleton branch.
func acceptanceGateTask(required bool) *task.Task {
	return &task.Task{
		ID:                 "acceptance-gate-test",
		AcceptanceCriteria: []task.AcceptanceCriterion{{ID: "AC1", Text: "x", Status: "pending"}},
		Preflight: &task.Preflight{AcceptanceSkeleton: &task.AcceptanceSkeletonConfig{
			Enabled:  true,
			Required: acceptanceGateBoolPtr(required),
		}},
	}
}

// writeAcceptanceGateRun seeds runs/<run-id>/preflight_result.json and the
// latest attempt's skeleton_checkpoint_results.json so evaluateAcceptanceGate
// reads the same evidence shape the daemon writes at runtime.
func writeAcceptanceGateRun(t *testing.T, status string, outputs []AcceptanceSkeletonOutput, checkpoints []CheckpointResult) string {
	t.Helper()
	runDir := t.TempDir()
	res := &AcceptanceSkeletonResult{
		Status:        status,
		SourceOfTruth: true,
		Outputs:       outputs,
		Baseline:      AcceptanceSkeletonBaseline{SkeletonHashes: []SkeletonHash{}},
	}
	if err := WritePreflightResult(runDir, res); err != nil {
		t.Fatal(err)
	}
	if err := WriteCheckpointResults(filepath.Join(runDir, "attempt-1"), checkpoints); err != nil {
		t.Fatal(err)
	}
	return runDir
}

func skeletonOutput() AcceptanceSkeletonOutput {
	return AcceptanceSkeletonOutput{
		ACID:                   "AC1",
		Path:                   "internal/foo/foo_test.go",
		Kind:                   "go-test",
		Purpose:                "verify AC1",
		ImplementationRequired: true,
		CheckpointCommand:      "go test ./internal/foo/",
	}
}

func TestAcceptanceGateDowngradesOnFailedSkeletonCheckpoint(t *testing.T) {
	t.Parallel()
	runDir := writeAcceptanceGateRun(t, "completed",
		[]AcceptanceSkeletonOutput{skeletonOutput()},
		[]CheckpointResult{{ACID: "AC1", Command: "go test ./internal/foo/", Status: "failed", ExitCode: 1, Source: "acceptance_skeleton"}},
	)
	reason, ok := evaluateAcceptanceGate(acceptanceGateTask(false), runDir)
	if ok {
		t.Fatalf("expected accepted verdict to be blocked, reason=%q", reason)
	}
	if !strings.Contains(reason, "AC1") || !strings.Contains(reason, "checkpoint") {
		t.Fatalf("reason missing skeleton checkpoint detail: %q", reason)
	}
}

func TestAcceptanceGateDowngradesOnMissingSkeletonCheckpoint(t *testing.T) {
	t.Parallel()
	// No checkpoint result recorded for the implementation_required output.
	runDir := writeAcceptanceGateRun(t, "completed",
		[]AcceptanceSkeletonOutput{skeletonOutput()},
		[]CheckpointResult{},
	)
	reason, ok := evaluateAcceptanceGate(acceptanceGateTask(true), runDir)
	if ok {
		t.Fatalf("expected missing checkpoint to block acceptance, reason=%q", reason)
	}
	if !strings.Contains(reason, "missing or failed") {
		t.Fatalf("reason missing detail: %q", reason)
	}
}

func TestAcceptanceGateAllowsWhenSkeletonCheckpointPassed(t *testing.T) {
	t.Parallel()
	runDir := writeAcceptanceGateRun(t, "completed",
		[]AcceptanceSkeletonOutput{skeletonOutput()},
		[]CheckpointResult{{ACID: "AC1", Command: "go test ./internal/foo/", Status: "passed", Source: "acceptance_skeleton"}},
	)
	reason, ok := evaluateAcceptanceGate(acceptanceGateTask(true), runDir)
	if !ok {
		t.Fatalf("expected gate to allow acceptance, reason=%q", reason)
	}
}

func TestAcceptanceGateDowngradesOnFailedPreflightResult(t *testing.T) {
	t.Parallel()
	runDir := t.TempDir()
	res := &AcceptanceSkeletonResult{
		Status:        "failed",
		SourceOfTruth: true,
		Baseline:      AcceptanceSkeletonBaseline{SkeletonHashes: []SkeletonHash{}},
		Error:         &AcceptanceSkeletonError{Phase: "acceptance_skeleton_creator", Message: "creator command exited 1"},
	}
	if err := WritePreflightResult(runDir, res); err != nil {
		t.Fatal(err)
	}
	reason, ok := evaluateAcceptanceGate(acceptanceGateTask(true), runDir)
	if ok || !strings.Contains(reason, "preflight failed") {
		t.Fatalf("ok=%v reason=%q", ok, reason)
	}
}

// The required quality-check evidence gate is part of the acceptance skeleton
// contract, so it only runs for preflight-enabled tasks (AC-001). These tests
// therefore exercise it through a preflight-enabled task.
func TestAcceptanceGateDowngradesOnMissingRequiredCheckEvidence(t *testing.T) {
	t.Parallel()
	runDir := t.TempDir()
	writeRunProfiles(t, runDir, []profile.RequiredCheck{{ID: "tests", Required: true, PreferredCommands: []string{"go test ./..."}}})
	// No attempt-N directory, so no executor result is available to verify.
	reason, ok := evaluateAcceptanceGate(acceptanceGateTask(false), runDir)
	if ok || !strings.Contains(reason, "required quality checks") {
		t.Fatalf("ok=%v reason=%q", ok, reason)
	}
}

func TestAcceptanceGateDowngradesOnFailedRequiredCheckEvidence(t *testing.T) {
	t.Parallel()
	runDir := t.TempDir()
	writeRunProfiles(t, runDir, []profile.RequiredCheck{{ID: "tests", Required: true, PreferredCommands: []string{"go test ./..."}}})
	writeAttemptResult(t, runDir, 1, []runner.ClaudeVerification{{Command: "go test ./...", Status: "failed"}})
	reason, ok := evaluateAcceptanceGate(acceptanceGateTask(false), runDir)
	if ok || !strings.Contains(reason, "failed verification evidence") {
		t.Fatalf("ok=%v reason=%q", ok, reason)
	}
}

func TestAcceptanceGateAllowsWhenRequiredCheckPassed(t *testing.T) {
	t.Parallel()
	runDir := writeAcceptanceGateRun(t, "completed",
		[]AcceptanceSkeletonOutput{skeletonOutput()},
		[]CheckpointResult{{ACID: "AC1", Command: "go test ./internal/foo/", Status: "passed", Source: "acceptance_skeleton"}},
	)
	writeRunProfiles(t, runDir, []profile.RequiredCheck{{ID: "tests", Required: true, PreferredCommands: []string{"go test ./..."}}})
	writeAttemptResult(t, runDir, 1, []runner.ClaudeVerification{{Command: "go test ./...", Status: "passed"}})
	reason, ok := evaluateAcceptanceGate(acceptanceGateTask(true), runDir)
	if !ok {
		t.Fatalf("expected required-check gate to allow acceptance, reason=%q", reason)
	}
}

// TestAcceptanceGateDefaultFlowIgnoresRequiredCheckEvidence proves that a task
// without preflight.acceptance_skeleton is never touched by the required
// quality-check evidence gate, even when required-check evidence is missing or
// failed — preserving the pre-feature daemon behavior (AC-001).
func TestAcceptanceGateDefaultFlowIgnoresRequiredCheckEvidence(t *testing.T) {
	t.Parallel()
	runDir := t.TempDir()
	writeRunProfiles(t, runDir, []profile.RequiredCheck{{ID: "tests", Required: true, PreferredCommands: []string{"go test ./..."}}})
	writeAttemptResult(t, runDir, 1, []runner.ClaudeVerification{{Command: "go test ./...", Status: "failed"}})

	if reason, ok := evaluateAcceptanceGate(&task.Task{ID: "no-preflight"}, runDir); !ok {
		t.Fatalf("default-flow task downgraded by acceptance gate: %q", reason)
	}
	// A disabled acceptance skeleton section is equivalent to omitting it.
	disabled := &task.Task{ID: "preflight-disabled", Preflight: &task.Preflight{AcceptanceSkeleton: &task.AcceptanceSkeletonConfig{Enabled: false}}}
	if reason, ok := evaluateAcceptanceGate(disabled, runDir); !ok {
		t.Fatalf("disabled-preflight task downgraded by acceptance gate: %q", reason)
	}
}

// TestAcceptanceGateLifecycleDowngradesAcceptedVerdictBeforeFinalize proves the
// daemon downgrades an accepted supervisor verdict to needs_supervisor_review —
// and never reaches acceptSupervisorVerdict / the "done" state — when required
// skeleton checkpoint evidence is missing or failed.
func TestAcceptanceGateLifecycleDowngradesAcceptedVerdictBeforeFinalize(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	runningDir := filepath.Join(root, "tasks", "running")
	if err := os.MkdirAll(runningDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runningPath := filepath.Join(runningDir, "task.yaml")
	loaded := acceptanceGateTask(true)
	loaded.Status = "running"
	loaded.Attempts = []task.Attempt{{Number: 1}}
	if err := task.Save(runningPath, *loaded); err != nil {
		t.Fatal(err)
	}
	runDir := writeAcceptanceGateRun(t, "completed",
		[]AcceptanceSkeletonOutput{skeletonOutput()},
		[]CheckpointResult{{ACID: "AC1", Command: "go test ./internal/foo/", Status: "failed", ExitCode: 1, Source: "acceptance_skeleton"}},
	)

	nextPrompt, done, err := applySupervisorVerdict(context.Background(), context.Background(), verdictApplication{
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
	if !done || nextPrompt != "" {
		t.Fatalf("done=%v nextPrompt=%q", done, nextPrompt)
	}
	if loaded.Status != "needs_supervisor_review" {
		t.Fatalf("status = %q; want needs_supervisor_review", loaded.Status)
	}
	// The downgrade must move the task to tasks/failed (not tasks/done).
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
	if len(saved.Risks) == 0 || !strings.Contains(saved.Risks[len(saved.Risks)-1].Detail, "acceptance skeleton gate") {
		t.Fatalf("expected acceptance-gate risk, got %#v", saved.Risks)
	}
}

// TestAcceptanceGateLifecyclePreservesAcceptedPathWithoutPreflight is the
// regression guard for the no-preflight default flow: a task that omits
// preflight.acceptance_skeleton must keep its existing accepted path through
// applySupervisorVerdict even when the resolved quality profile has required
// checks whose evidence is missing or failed. The required quality-check
// evidence gate is part of the acceptance skeleton contract and must not be
// applied to default-flow tasks unless a human revised the task contract.
func TestAcceptanceGateLifecyclePreservesAcceptedPathWithoutPreflight(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name          string
		verifications []runner.ClaudeVerification
		writeResult   bool
	}{
		{name: "failed required-check evidence", verifications: []runner.ClaudeVerification{{Command: "go test ./...", Status: "failed"}}, writeResult: true},
		{name: "missing required-check evidence", writeResult: false},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			runningDir := filepath.Join(root, "tasks", "running")
			if err := os.MkdirAll(runningDir, 0o755); err != nil {
				t.Fatal(err)
			}
			runningPath := filepath.Join(runningDir, "task.yaml")
			loaded := &task.Task{
				ID:                 "no-preflight-default-flow",
				Status:             "running",
				AcceptanceCriteria: []task.AcceptanceCriterion{{ID: "AC1", Text: "x", Status: "pending"}},
				Attempts:           []task.Attempt{{Number: 1}},
			}
			if err := task.Save(runningPath, *loaded); err != nil {
				t.Fatal(err)
			}
			runDir := t.TempDir()
			writeRunProfiles(t, runDir, []profile.RequiredCheck{{ID: "tests", Required: true, PreferredCommands: []string{"go test ./..."}}})
			if tc.writeResult {
				writeAttemptResult(t, runDir, 1, tc.verifications)
			}

			nextPrompt, done, err := applySupervisorVerdict(context.Background(), context.Background(), verdictApplication{
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
			if !done || nextPrompt != "" {
				t.Fatalf("done=%v nextPrompt=%q", done, nextPrompt)
			}
			if loaded.Status != "accepted" {
				t.Fatalf("status = %q; want accepted", loaded.Status)
			}
			if _, statErr := os.Stat(filepath.Join(root, "tasks", "done", "task.yaml")); statErr != nil {
				t.Fatalf("accepted default-flow task not in tasks/done: %v", statErr)
			}
			if _, statErr := os.Stat(filepath.Join(root, "tasks", "failed", "task.yaml")); statErr == nil {
				t.Fatal("default-flow accepted task must not be downgraded to tasks/failed")
			}
			saved, err := task.Load(filepath.Join(root, "tasks", "done", "task.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range saved.Risks {
				if strings.Contains(r.Detail, "acceptance skeleton gate") || strings.Contains(r.Detail, "required check") {
					t.Fatalf("default-flow task gained an acceptance-gate risk: %#v", r)
				}
			}
		})
	}
}
