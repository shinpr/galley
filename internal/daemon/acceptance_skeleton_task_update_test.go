package daemon

import (
	"strings"
	"testing"

	"github.com/shinpr/galley/internal/task"
)

func TestApplyAcceptanceSkeletonResultToTaskWritesOutputsAndACVerification(t *testing.T) {
	t.Parallel()
	loaded := task.Task{
		AcceptanceCriteria: []task.AcceptanceCriterion{{
			ID:           "AC1",
			Text:         "behavior",
			Verification: "existing verification",
			Status:       "pending",
		}},
		Preflight: &task.Preflight{AcceptanceSkeleton: &task.AcceptanceSkeletonConfig{Enabled: true}},
	}
	res := &AcceptanceSkeletonResult{Outputs: []AcceptanceSkeletonOutput{{
		ACID:                   "AC1",
		Path:                   "internal/foo/foo_test.go",
		Kind:                   "go-test",
		Purpose:                "verify foo",
		Satisfies:              "covers the AC1 observable behavior",
		IntegrationPoint:       "executor completes this skeleton before acceptance",
		ImplementationRequired: true,
	}}}

	applyAcceptanceSkeletonResultToTask(&loaded, res)

	outputs := loaded.Preflight.AcceptanceSkeleton.Outputs
	if len(outputs) != 1 || outputs[0].Path != "internal/foo/foo_test.go" || outputs[0].Satisfies == "" || outputs[0].IntegrationPoint == "" {
		t.Fatalf("outputs not written to task: %+v", outputs)
	}
	verification := loaded.AcceptanceCriteria[0].Verification
	for _, want := range []string{"existing verification", "Acceptance skeleton:", "covers the AC1 observable behavior", "integration point:"} {
		if !strings.Contains(verification, want) {
			t.Fatalf("verification missing %q:\n%s", want, verification)
		}
	}
}

func TestApplyAcceptanceSkeletonResultToTaskReplacesPreviousSkeletonBlock(t *testing.T) {
	t.Parallel()
	for _, existing := range []string{
		"base\n\nAcceptance skeleton:\n- stale",
		"Acceptance skeleton:\n- stale",
	} {
		loaded := task.Task{
			AcceptanceCriteria: []task.AcceptanceCriterion{{
				ID:           "AC1",
				Verification: existing,
			}},
			Preflight: &task.Preflight{AcceptanceSkeleton: &task.AcceptanceSkeletonConfig{Enabled: true}},
		}
		res := &AcceptanceSkeletonResult{Outputs: []AcceptanceSkeletonOutput{{ACID: "AC1", Path: "new_test.go", Purpose: "new"}}}
		applyAcceptanceSkeletonResultToTask(&loaded, res)
		got := loaded.AcceptanceCriteria[0].Verification
		if strings.Contains(got, "stale") || strings.Count(got, "Acceptance skeleton:") != 1 || !strings.Contains(got, "new_test.go") {
			t.Fatalf("unexpected verification for %q:\n%s", existing, got)
		}
	}
}
