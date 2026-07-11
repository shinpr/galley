package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	skeletonpreflight "github.com/shinpr/galley/internal/preflight/skeleton"
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

func TestAcceptanceGateDowngradesOnMissingRequiredSkeletonCoverage(t *testing.T) {
	t.Parallel()
	runDir := writeAcceptanceGateRun(t, "completed", nil)
	reason, ok := evaluateAcceptanceGate(acceptanceGateTask(true), runDir)
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
	reason, ok := evaluateAcceptanceGate(acceptanceGateTask(true), runDir)
	if !ok {
		t.Fatalf("expected gate to allow acceptance, reason=%q", reason)
	}
}

func TestAcceptanceGateDowngradesOnFailedPreflightResult(t *testing.T) {
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
	writeAttemptResult(t, runDir, 1, []runner.ExecutorVerification{{Command: "go test ./...", Status: "failed"}})
	reason, ok := evaluateAcceptanceGate(acceptanceGateTask(false), runDir)
	if ok || !strings.Contains(reason, "failed verification evidence") {
		t.Fatalf("ok=%v reason=%q", ok, reason)
	}
}

func TestAcceptanceGateAllowsWhenRequiredCheckPassed(t *testing.T) {
	t.Parallel()
	runDir := writeAcceptanceGateRun(t, "completed", []skeletonpreflight.Output{skeletonOutput()})
	writeRunProfiles(t, runDir, []profile.RequiredCheck{{ID: "tests", Required: true, PreferredCommands: []string{"go test ./..."}}})
	writeAttemptResult(t, runDir, 1, []runner.ExecutorVerification{{Command: "go test ./...", Status: "passed"}})
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
	writeAttemptResult(t, runDir, 1, []runner.ExecutorVerification{{Command: "go test ./...", Status: "failed"}})

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
// skeleton coverage is missing.
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
	runDir := writeAcceptanceGateRun(t, "completed", nil)

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
		verifications []runner.ExecutorVerification
		writeResult   bool
	}{
		{name: "failed required-check evidence", verifications: []runner.ExecutorVerification{{Command: "go test ./...", Status: "failed"}}, writeResult: true},
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
