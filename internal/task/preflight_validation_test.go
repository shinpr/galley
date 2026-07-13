package task

import (
	"strings"
	"testing"
)

// baseValidPreflightTask returns a task that passes ValidateStructural so
// individual cases can mutate only the preflight section.
func baseValidPreflightTask(t *testing.T) Task {
	t.Helper()
	return Task{
		ID:     "task-preflight-001",
		Mode:   "afk",
		Status: "queued",
		Goal:   "test goal",
		AcceptanceCriteria: []AcceptanceCriterion{{
			ID:           "AC1",
			Text:         "behavior",
			Verification: "go test ./...",
			Status:       "pending",
		}},
		Scope: Scope{
			CWD:            t.TempDir(),
			AllowedPaths:   []string{"internal", "tests"},
			ForbiddenPaths: []string{".env"},
			Permission:     "edit",
		},
		ExecutionPolicy: ExecutionPolicy{
			LoopBudget:        LoopBudget{Count: 1, Set: true},
			TimeoutMS:         1000,
			AFKDecisionPolicy: "choose-smallest-reversible",
		},
		Worktree: Worktree{
			Enabled: true,
			Branch:  "agent/task-preflight-001",
			Path:    "../repo.worktrees/task",
		},
		Supervisor: Supervisor{ReviewIterations: 0},
		Executor: Executor{
			CLI:    "claude",
			Model:  "opus",
			Effort: "high",
		},
	}
}

func TestPreflightValidationAbsentIsValid(t *testing.T) {
	tk := baseValidPreflightTask(t)
	res := ValidateStructural(tk)
	if !res.Valid() {
		t.Fatalf("expected valid task without preflight, got errors %v", res.Errors)
	}
}

func TestPreflightValidationModeRejectsUnknown(t *testing.T) {
	tk := baseValidPreflightTask(t)
	tk.Preflight = &Preflight{AcceptanceSkeleton: &AcceptanceSkeletonConfig{
		Enabled: true,
		Mode:    "deadbeef",
	}}
	res := ValidateStructural(tk)
	if res.Valid() {
		t.Fatalf("expected validation error for unknown mode, got none")
	}
	if !containsErr(res.Errors, "preflight.acceptance_skeleton.mode") {
		t.Fatalf("expected mode validation error, got %v", res.Errors)
	}
}

func TestPreflightValidationAllowedPathOutsideScopeIsRejected(t *testing.T) {
	tk := baseValidPreflightTask(t)
	tk.Preflight = &Preflight{AcceptanceSkeleton: &AcceptanceSkeletonConfig{
		Enabled:      true,
		Mode:         "skeleton",
		AllowedPaths: []string{"outside"},
	}}
	res := ValidateStructural(tk)
	if res.Valid() {
		t.Fatalf("expected validation error, got none")
	}
	if !containsErr(res.Errors, "must be inside scope.allowed_paths") {
		t.Fatalf("expected allowed_paths error, got %v", res.Errors)
	}
}

func TestPreflightValidationAllowedPathForbiddenIsRejected(t *testing.T) {
	tk := baseValidPreflightTask(t)
	tk.Scope.AllowedPaths = []string{"."}
	tk.Preflight = &Preflight{AcceptanceSkeleton: &AcceptanceSkeletonConfig{
		Enabled:      true,
		Mode:         "skeleton",
		AllowedPaths: []string{".env"},
	}}
	res := ValidateStructural(tk)
	if res.Valid() {
		t.Fatalf("expected validation error, got none")
	}
	if !containsErr(res.Errors, "must not be inside scope.forbidden_paths") {
		t.Fatalf("expected forbidden_paths error, got %v", res.Errors)
	}
}

func TestPreflightValidationAllowedSubsetAccepted(t *testing.T) {
	tk := baseValidPreflightTask(t)
	tk.Preflight = &Preflight{AcceptanceSkeleton: &AcceptanceSkeletonConfig{
		Enabled:      true,
		Mode:         "skeleton",
		AllowedPaths: []string{"internal"},
	}}
	res := ValidateStructural(tk)
	if !res.Valid() {
		t.Fatalf("expected valid preflight, got %v", res.Errors)
	}
}

func TestPreflightValidationDuplicateOutputPathsAcrossACsAccepted(t *testing.T) {
	tk := baseValidPreflightTask(t)
	tk.AcceptanceCriteria = []AcceptanceCriterion{
		{ID: "AC1", Text: "AC1 behavior", Verification: "go test ./internal/foo", Status: "pending"},
		{ID: "AC2", Text: "AC2 behavior", Verification: "go test ./internal/foo", Status: "pending"},
	}
	tk.Preflight = &Preflight{AcceptanceSkeleton: &AcceptanceSkeletonConfig{
		Enabled: true,
		Mode:    "skeleton",
		Outputs: []AcceptanceSkeletonOutputDef{
			{
				ACID:    "AC1",
				Path:    "internal/foo/foo_test.go",
				Kind:    "go-test",
				Purpose: "verify AC1 behavior",
			},
			{
				ACID:    "AC2",
				Path:    "internal/foo/foo_test.go",
				Kind:    "go-test",
				Purpose: "verify AC2 behavior",
			},
		},
	}}
	res := ValidateStructural(tk)
	if !res.Valid() {
		t.Fatalf("expected duplicate output paths across distinct ACs to validate, got errors %v", res.Errors)
	}
	for _, e := range res.Errors {
		if strings.Contains(e, "duplicated") {
			t.Fatalf("expected no duplicated-path error, got %q", e)
		}
	}
}

func TestPreflightValidationDuplicateOutputPathsSeparatorEquivalentAccepted(t *testing.T) {
	tk := baseValidPreflightTask(t)
	tk.AcceptanceCriteria = []AcceptanceCriterion{
		{ID: "AC1", Text: "AC1 behavior", Verification: "go test ./internal/foo", Status: "pending"},
		{ID: "AC2", Text: "AC2 behavior", Verification: "go test ./internal/foo", Status: "pending"},
	}
	tk.Preflight = &Preflight{AcceptanceSkeleton: &AcceptanceSkeletonConfig{
		Enabled: true,
		Mode:    "skeleton",
		Outputs: []AcceptanceSkeletonOutputDef{
			{
				ACID:    "AC1",
				Path:    "internal/foo/foo_test.go",
				Kind:    "go-test",
				Purpose: "verify AC1 behavior",
			},
			{
				ACID:    "AC2",
				Path:    "internal\\foo\\foo_test.go",
				Kind:    "go-test",
				Purpose: "verify AC2 behavior",
			},
		},
	}}
	res := ValidateStructural(tk)
	if !res.Valid() {
		t.Fatalf("expected separator-equivalent duplicate output paths to validate, got errors %v", res.Errors)
	}
}

func TestPreflightValidationDuplicateOutputPathStillEnforcesSafetyChecks(t *testing.T) {
	tk := baseValidPreflightTask(t)
	tk.AcceptanceCriteria = []AcceptanceCriterion{
		{ID: "AC1", Text: "AC1 behavior", Verification: "go test ./internal/foo", Status: "pending"},
		{ID: "AC2", Text: "AC2 behavior", Verification: "go test ./internal/foo", Status: "pending"},
	}
	tk.Preflight = &Preflight{AcceptanceSkeleton: &AcceptanceSkeletonConfig{
		Enabled: true,
		Mode:    "skeleton",
		Outputs: []AcceptanceSkeletonOutputDef{
			{
				ACID:    "AC1",
				Path:    "internal/foo/foo_test.go",
				Kind:    "go-test",
				Purpose: "verify AC1 behavior",
			},
			{
				// Same logical path as AC1 above, but the entry is missing
				// required metadata. Duplicate paths must be accepted while
				// per-entry checks still reject incomplete declarations.
				ACID: "AC2",
				Path: "internal/foo/foo_test.go",
			},
		},
	}}
	res := ValidateStructural(tk)
	if res.Valid() {
		t.Fatalf("expected validation error for missing required output fields, got none")
	}
	if containsErr(res.Errors, "duplicated") {
		t.Fatalf("did not expect duplicate-path error, got %v", res.Errors)
	}
	if !containsErr(res.Errors, "outputs[1].kind is required") {
		t.Fatalf("expected outputs[1].kind required error, got %v", res.Errors)
	}
	if !containsErr(res.Errors, "outputs[1].purpose is required") {
		t.Fatalf("expected outputs[1].purpose required error, got %v", res.Errors)
	}
}

func TestPreflightValidationDuplicateOutputPathStillEnforcesPathSafety(t *testing.T) {
	tk := baseValidPreflightTask(t)
	tk.AcceptanceCriteria = []AcceptanceCriterion{
		{ID: "AC1", Text: "AC1 behavior", Verification: "go test ./internal/foo", Status: "pending"},
		{ID: "AC2", Text: "AC2 behavior", Verification: "go test ./internal/foo", Status: "pending"},
	}
	tk.Preflight = &Preflight{AcceptanceSkeleton: &AcceptanceSkeletonConfig{
		Enabled: true,
		Mode:    "skeleton",
		Outputs: []AcceptanceSkeletonOutputDef{
			{
				ACID:    "AC1",
				Path:    "internal/foo/foo_test.go",
				Kind:    "go-test",
				Purpose: "verify AC1 behavior",
			},
			{
				// Duplicate-path allowance must not bypass per-entry path
				// safety checks; unsafe parent traversal is still rejected.
				ACID:    "AC2",
				Path:    "../foo/foo_test.go",
				Kind:    "go-test",
				Purpose: "verify AC2 behavior",
			},
		},
	}}
	res := ValidateStructural(tk)
	if res.Valid() {
		t.Fatalf("expected validation error for parent-traversal output path, got none")
	}
	if !containsErr(res.Errors, "parent traversal") {
		t.Fatalf("expected parent traversal error, got %v", res.Errors)
	}
}

func TestAcceptanceSkeletonConfigDefaults(t *testing.T) {
	var nilCfg *AcceptanceSkeletonConfig
	if nilCfg.IsEnabled() {
		t.Fatalf("nil config should not be enabled")
	}
	if nilCfg.IsRequired() {
		t.Fatalf("nil config should not be required")
	}
	cfg := &AcceptanceSkeletonConfig{Enabled: true}
	if !cfg.IsRequired() {
		t.Fatalf("enabled config should default to required")
	}
	val := false
	cfg.Required = &val
	if cfg.IsRequired() {
		t.Fatalf("explicit false required should override default")
	}
}

func containsErr(errs []string, needle string) bool {
	for _, e := range errs {
		if strings.Contains(e, needle) {
			return true
		}
	}
	return false
}
