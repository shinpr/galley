package task

import (
	"strings"
	"testing"
)

// baseValidPreflightTask returns a task that passes ValidateStructural so
// individual cases can mutate only the preflight section.
func baseValidPreflightTask() Task {
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
			CWD:            "/tmp/repo",
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
			CLI:           "claude",
			Model:         "opus",
			Effort:        "high",
			PromptProfile: "p",
			PromptMode:    "replace",
			MaxBudgetUSD:  float64Ptr(1),
		},
	}
}

func TestPreflightValidationAbsentIsValid(t *testing.T) {
	tk := baseValidPreflightTask()
	res := ValidateStructural(tk)
	if !res.Valid() {
		t.Fatalf("expected valid task without preflight, got errors %v", res.Errors)
	}
}

func TestPreflightValidationModeRejectsUnknown(t *testing.T) {
	tk := baseValidPreflightTask()
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
	tk := baseValidPreflightTask()
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
	tk := baseValidPreflightTask()
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
	tk := baseValidPreflightTask()
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
