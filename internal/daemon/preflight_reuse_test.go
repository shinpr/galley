package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	setuppreflight "github.com/shinpr/galley/internal/preflight/setup"
	skeletonpreflight "github.com/shinpr/galley/internal/preflight/skeleton"
	"github.com/shinpr/galley/internal/runartifact"
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

	setupRes, setupReused, err := reuseReadySetup(root, taskID, currentRun, effective, "test")
	if err != nil || !setupReused || setupRes.ReadinessEvidence != "ready once" {
		t.Fatalf("setup reuse = (%+v, %v, %v)", setupRes, setupReused, err)
	}
	skeletonRes, skeletonReused, err := reuseCompletedAcceptanceSkeleton(root, taskID, currentRun, effective, "test", "")
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

	if res, reused, err := reuseReadySetup(root, "task-new", runDir, effective, "test"); err != nil || reused || res != nil {
		t.Fatalf("setup reuse = (%+v, %v, %v)", res, reused, err)
	}
	if res, reused, err := reuseCompletedAcceptanceSkeleton(root, "task-new", runDir, effective, "test", ""); err != nil || reused || res != nil {
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

	if res, reused, err := reuseReadySetup(root, taskID, currentRun, current, "test"); err != nil || reused || res != nil {
		t.Fatalf("setup should not reuse mismatched identity: (%+v, %v, %v)", res, reused, err)
	}
	if res, reused, err := reuseCompletedAcceptanceSkeleton(root, taskID, currentRun, current, "test", ""); err != nil || reused || res != nil {
		t.Fatalf("skeleton should not reuse mismatched identity: (%+v, %v, %v)", res, reused, err)
	}
}

func TestReuseCompletedPreflightsDerivesSetupCLIFromProvider(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	taskID := "task-legacy-provider"
	priorRun := filepath.Join(root, "runs", taskID+"-1")
	currentRun := filepath.Join(root, "runs", taskID+"-2")
	// Legacy evidence only recorded Provider; model/effort were not stamped.
	// Derive CLI from Provider, but still require model/effort to match, so a
	// fully resolved current identity without matching blanks will not reuse.
	legacy := &setuppreflight.Result{
		Status:   setuppreflight.StatusReady,
		Provider: "claude",
	}
	if err := writeSetupReuseTestResult(t, priorRun, legacy); err != nil {
		t.Fatal(err)
	}
	// Match when current also has empty model/effort (only CLI resolved).
	matchEmpty := task.Executor{CLI: "claude"}
	if res, reused, err := reuseReadySetup(root, taskID, currentRun, matchEmpty, "test"); err != nil || !reused || res == nil {
		t.Fatalf("expected provider-derived CLI match with empty model/effort: (%+v, %v, %v)", res, reused, err)
	}
	// Mismatch when current carries resolved model/effort that legacy lacks.
	currentRun2 := filepath.Join(root, "runs", taskID+"-3")
	full := task.Executor{CLI: "claude", Model: "m", Effort: "high"}
	if res, reused, err := reuseReadySetup(root, taskID, currentRun2, full, "test"); err != nil || reused || res != nil {
		t.Fatalf("legacy provider-only evidence must not match full identity: (%+v, %v, %v)", res, reused, err)
	}
}

func TestReuseCompletedPreflightsMatchesExplicitEmptyModel(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	taskID := "task-empty-model"
	priorRun := filepath.Join(root, "runs", taskID+"-1")
	currentRun := filepath.Join(root, "runs", taskID+"-2")
	// Explicit empty model is a resolved identity (CLI default), not "unknown".
	effective := task.Executor{CLI: "grok", Model: "", Effort: "low"}

	setupRes := &setuppreflight.Result{Status: setuppreflight.StatusReady, ReadinessEvidence: "empty-model ready"}
	setuppreflight.ApplyExecutorIdentity(setupRes, effective)
	if err := writeSetupReuseTestResult(t, priorRun, setupRes); err != nil {
		t.Fatal(err)
	}
	skeletonRes := &skeletonpreflight.Result{Status: "completed", SourceOfTruth: true}
	skeletonpreflight.ApplyExecutorIdentity(skeletonRes, effective)
	if err := writeSkeletonReuseTestResult(t, priorRun, skeletonRes); err != nil {
		t.Fatal(err)
	}

	// Same empty model reuses both preflights.
	gotSetup, setupReused, err := reuseReadySetup(root, taskID, currentRun, effective, "test")
	if err != nil || !setupReused || gotSetup == nil || gotSetup.ExecutorModel != "" {
		t.Fatalf("setup empty-model reuse = (%+v, %v, %v)", gotSetup, setupReused, err)
	}
	gotSkeleton, skeletonReused, err := reuseCompletedAcceptanceSkeleton(root, taskID, currentRun, effective, "test", "")
	if err != nil || !skeletonReused || gotSkeleton == nil || gotSkeleton.ExecutorModel != "" {
		t.Fatalf("skeleton empty-model reuse = (%+v, %v, %v)", gotSkeleton, skeletonReused, err)
	}

	// A later non-empty model must invalidate the empty-model evidence.
	currentRun3 := filepath.Join(root, "runs", taskID+"-3")
	withModel := task.Executor{CLI: "grok", Model: "grok-code", Effort: "low"}
	if res, reused, err := reuseReadySetup(root, taskID, currentRun3, withModel, "test"); err != nil || reused || res != nil {
		t.Fatalf("setup must not reuse empty-model evidence for non-empty model: (%+v, %v, %v)", res, reused, err)
	}
	if res, reused, err := reuseCompletedAcceptanceSkeleton(root, taskID, currentRun3, withModel, "test", ""); err != nil || reused || res != nil {
		t.Fatalf("skeleton must not reuse empty-model evidence for non-empty model: (%+v, %v, %v)", res, reused, err)
	}
}

func TestReuseCompletedPreflightsEffortIdentityTransitions(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		prior     string
		current   string
		wantReuse bool
	}{
		{"empty prior reused for empty current", "", "", true},
		{"empty prior reruns for explicit current", "", "high", false},
		{"explicit prior reruns for empty current", "high", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			taskID := "task-effort-transition"
			priorRun := filepath.Join(root, "runs", taskID+"-1")
			currentRun := filepath.Join(root, "runs", taskID+"-2")
			// CLI and model are held identical so only the effort transition drives reuse.
			prior := task.Executor{CLI: "claude", Model: "opus", Effort: tc.prior}
			current := task.Executor{CLI: "claude", Model: "opus", Effort: tc.current}

			setupRes := &setuppreflight.Result{Status: setuppreflight.StatusReady, ReadinessEvidence: "ready"}
			setuppreflight.ApplyExecutorIdentity(setupRes, prior)
			if err := writeSetupReuseTestResult(t, priorRun, setupRes); err != nil {
				t.Fatal(err)
			}
			skeletonRes := &skeletonpreflight.Result{Status: "completed", SourceOfTruth: true}
			skeletonpreflight.ApplyExecutorIdentity(skeletonRes, prior)
			if err := writeSkeletonReuseTestResult(t, priorRun, skeletonRes); err != nil {
				t.Fatal(err)
			}

			if tc.prior == "" {
				assertPersistedExecutorEffortEmpty(t, filepath.Join(priorRun, runartifact.SetupResultFilename))
				assertPersistedExecutorEffortEmpty(t, filepath.Join(priorRun, runartifact.PreflightResultFilename))
			}

			_, setupReused, err := reuseReadySetup(root, taskID, currentRun, current, "test")
			if err != nil {
				t.Fatalf("setup reuse err: %v", err)
			}
			if setupReused != tc.wantReuse {
				t.Fatalf("setup reuse = %v, want %v (prior effort %q, current effort %q)", setupReused, tc.wantReuse, tc.prior, tc.current)
			}
			_, skeletonReused, err := reuseCompletedAcceptanceSkeleton(root, taskID, currentRun, current, "test", "")
			if err != nil {
				t.Fatalf("skeleton reuse err: %v", err)
			}
			if skeletonReused != tc.wantReuse {
				t.Fatalf("skeleton reuse = %v, want %v (prior effort %q, current effort %q)", skeletonReused, tc.wantReuse, tc.prior, tc.current)
			}
		})
	}
}

func assertPersistedExecutorEffortEmpty(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}
	v, ok := raw["executor_effort"]
	if !ok || string(v) != `""` {
		t.Fatalf("%s must persist an explicit empty executor_effort, got %q (present=%v)", path, string(v), ok)
	}
}
