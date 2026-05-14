package daemon

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/shinpr/galley/internal/profile"
	"github.com/shinpr/galley/internal/runner"
	"github.com/shinpr/galley/internal/task"
)

func writeRunProfiles(t *testing.T, runDir string, checks []profile.RequiredCheck) {
	t.Helper()
	payload := struct {
		Bundle profile.Bundle `json:"bundle"`
	}{Bundle: profile.Bundle{Quality: &profile.Quality{ID: "q", RequiredChecks: checks}}}
	if err := writeJSON(filepath.Join(runDir, "profiles.json"), payload); err != nil {
		t.Fatal(err)
	}
}

func writeAttemptResult(t *testing.T, runDir string, n int, verifications []runner.ClaudeVerification) {
	writeAttemptResultFile(t, runDir, n, executorResultFilename, verifications)
}

func writeAttemptResultFile(t *testing.T, runDir string, n int, filename string, verifications []runner.ClaudeVerification) {
	t.Helper()
	dir := filepath.Join(runDir, "attempt-"+strconv.Itoa(n))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	res := runner.ClaudeResult{Status: "completed", Summary: "x", Verification: verifications}
	if err := writeJSON(filepath.Join(dir, filename), res); err != nil {
		t.Fatal(err)
	}
}

// TestRequiredCheckEvidenceGateFallbackSemantics verifies that a required
// check with multiple preferred commands is satisfied when *any* preferred
// command has passing evidence — matching result.runRequiredCheck which stops
// at the first passing command and records only that one. A multi-command
// check must not be downgraded just because later fallback commands have no
// recorded evidence.
func TestRequiredCheckEvidenceGateFallbackSemantics(t *testing.T) {
	t.Parallel()
	runDir := t.TempDir()
	writeRunProfiles(t, runDir, []profile.RequiredCheck{
		{ID: "test", PreferredCommands: []string{"make test", "go test ./..."}, Required: true},
	})
	writeAttemptResult(t, runDir, 1, []runner.ClaudeVerification{
		{Command: "make test", Status: "passed", Reason: "ok"},
	})
	reason, ok := requiredCheckEvidenceGate(&task.Task{}, runDir)
	if !ok {
		t.Fatalf("gate failed unexpectedly: %s", reason)
	}
}

func TestRequiredCheckEvidenceGateReadsLegacyClaudeResult(t *testing.T) {
	t.Parallel()
	runDir := t.TempDir()
	writeRunProfiles(t, runDir, []profile.RequiredCheck{
		{ID: "test", PreferredCommands: []string{"make test"}, Required: true},
	})
	writeAttemptResultFile(t, runDir, 1, legacyClaudeResultFilename, []runner.ClaudeVerification{
		{Command: "make test", Status: "passed", Reason: "ok"},
	})
	reason, ok := requiredCheckEvidenceGate(&task.Task{}, runDir)
	if !ok {
		t.Fatalf("gate failed for legacy executor result: %s", reason)
	}
}

// TestRequiredCheckEvidenceGateFailsWhenNoPass verifies the gate downgrades
// when the recorded evidence for a required check is a failure and no
// preferred command passed.
func TestRequiredCheckEvidenceGateFailsWhenNoPass(t *testing.T) {
	t.Parallel()
	runDir := t.TempDir()
	writeRunProfiles(t, runDir, []profile.RequiredCheck{
		{ID: "test", PreferredCommands: []string{"make test", "go test ./..."}, Required: true},
	})
	writeAttemptResult(t, runDir, 1, []runner.ClaudeVerification{
		{Command: "go test ./...", Status: "failed", Reason: "boom"},
	})
	reason, ok := requiredCheckEvidenceGate(&task.Task{}, runDir)
	if ok {
		t.Fatalf("gate passed unexpectedly")
	}
	if reason == "" {
		t.Fatalf("expected a non-empty downgrade reason")
	}
}

// TestRequiredCheckEvidenceGateFailsWhenNoEvidence verifies the gate
// downgrades when there is no verification evidence at all for a required
// check (e.g. the executor hard-stopped before result.Complete ran).
func TestRequiredCheckEvidenceGateFailsWhenNoEvidence(t *testing.T) {
	t.Parallel()
	runDir := t.TempDir()
	writeRunProfiles(t, runDir, []profile.RequiredCheck{
		{ID: "test", PreferredCommands: []string{"make test"}, Required: true},
	})
	writeAttemptResult(t, runDir, 1, []runner.ClaudeVerification{
		{Command: "lint", Status: "passed", Reason: "ok"},
	})
	reason, ok := requiredCheckEvidenceGate(&task.Task{}, runDir)
	if ok {
		t.Fatalf("gate passed unexpectedly: %s", reason)
	}
}

// TestRequiredCheckEvidenceGateNoQualityProfilePasses verifies legacy flows
// without a resolved quality profile are unaffected.
func TestRequiredCheckEvidenceGateNoQualityProfilePasses(t *testing.T) {
	t.Parallel()
	runDir := t.TempDir()
	reason, ok := requiredCheckEvidenceGate(&task.Task{}, runDir)
	if !ok {
		t.Fatalf("gate failed for missing profile: %s", reason)
	}
}
