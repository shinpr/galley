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

func TestApplyAcceptanceSkeletonResultToTaskPreservesDuplicatePathOutputs(t *testing.T) {
	t.Parallel()
	loaded := task.Task{
		AcceptanceCriteria: []task.AcceptanceCriterion{
			{ID: "AC1", Verification: "existing AC1 verification"},
			{ID: "AC2", Verification: "existing AC2 verification"},
		},
		Preflight: &task.Preflight{AcceptanceSkeleton: &task.AcceptanceSkeletonConfig{Enabled: true}},
	}
	res := &AcceptanceSkeletonResult{Outputs: []AcceptanceSkeletonOutput{
		{
			ACID:                   "AC1",
			Path:                   "internal/foo/foo_test.go",
			Kind:                   "go-test",
			Purpose:                "verify AC1 user value",
			Satisfies:              "covers AC1 user-visible behavior",
			IntegrationPoint:       "executor finishes AC1 case before acceptance",
			ImplementationRequired: true,
		},
		{
			ACID:                   "AC2",
			Path:                   "internal/foo/foo_test.go",
			Kind:                   "go-test",
			Purpose:                "verify AC2 user value",
			Satisfies:              "covers AC2 user-visible behavior",
			IntegrationPoint:       "executor finishes AC2 case before acceptance",
			ImplementationRequired: true,
		},
	}}

	applyAcceptanceSkeletonResultToTask(&loaded, res)

	outputs := loaded.Preflight.AcceptanceSkeleton.Outputs
	if len(outputs) != 2 {
		t.Fatalf("expected 2 task outputs, got %+v", outputs)
	}
	byAC := map[string]task.AcceptanceSkeletonOutputDef{}
	for _, out := range outputs {
		byAC[out.ACID] = out
	}
	for _, ac := range []string{"AC1", "AC2"} {
		out, ok := byAC[ac]
		if !ok {
			t.Fatalf("task outputs missing %s: %+v", ac, outputs)
		}
		if out.Path != "internal/foo/foo_test.go" {
			t.Fatalf("%s path = %q, want shared skeleton path", ac, out.Path)
		}
		if !strings.Contains(out.Purpose, ac) || !strings.Contains(out.Satisfies, ac) || !strings.Contains(out.IntegrationPoint, ac) {
			t.Fatalf("%s metadata mixed up in task outputs: %+v", ac, out)
		}
	}
	for i, ac := range []string{"AC1", "AC2"} {
		v := loaded.AcceptanceCriteria[i].Verification
		for _, want := range []string{"existing " + ac + " verification", "Acceptance skeleton:", "covers " + ac + " user-visible behavior", "integration point: executor finishes " + ac + " case before acceptance"} {
			if !strings.Contains(v, want) {
				t.Fatalf("%s verification missing %q:\n%s", ac, want, v)
			}
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
