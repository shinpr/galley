package supervisor

import (
	"testing"

	"github.com/shinpr/galley/internal/profile"
	"github.com/shinpr/galley/internal/task"
)

func TestApplyReviewProgressNarrowsAndReopensReviewScope(t *testing.T) {
	quality := &profile.Quality{ReviewDimensions: []profile.ReviewDimension{
		{ID: "quality-a", Pass: "quality a passes"},
		{ID: "quality-b", Pass: "quality b passes"},
	}}
	loaded := task.Task{AcceptanceCriteria: []task.AcceptanceCriterion{
		{ID: "AC1", Text: "first", Verification: "verify first", Status: "pending"},
		{ID: "AC2", Text: "second", Verification: "verify second", Status: "pending"},
	}}

	ApplyReviewProgress(&loaded, profile.Bundle{Quality: quality}, Verdict{
		AcceptanceGaps:     []string{"AC2"},
		AcceptanceEvidence: []AcceptanceEvidence{{ACID: "AC1", Evidence: []string{"first evidence"}}},
		QualityPasses:      []string{"quality-a"},
		QualityGaps:        []string{"quality-b"},
		Findings:           []Finding{{Category: "quality-b", Summary: "quality b fails"}},
	})

	assertReviewPassIDs(t, loaded.ReviewProgress.Acceptance, []string{"AC1"})
	assertReviewPassIDs(t, loaded.ReviewProgress.Quality, []string{"quality-a"})
	if loaded.AcceptanceCriteria[0].Status != "satisfied" || loaded.AcceptanceCriteria[1].Status != "not_satisfied" {
		t.Fatalf("acceptance statuses = %#v", loaded.AcceptanceCriteria)
	}

	ApplyReviewProgress(&loaded, profile.Bundle{Quality: quality}, Verdict{
		AcceptanceEvidence: []AcceptanceEvidence{{ACID: "AC2", Evidence: []string{"second evidence"}}},
		QualityPasses:      []string{"quality-b"},
		QualityGaps:        []string{},
	})

	assertReviewPassIDs(t, loaded.ReviewProgress.Acceptance, []string{"AC1", "AC2"})
	assertReviewPassIDs(t, loaded.ReviewProgress.Quality, []string{"quality-a", "quality-b"})

	ApplyReviewProgress(&loaded, profile.Bundle{Quality: quality}, Verdict{
		AcceptanceGaps: []string{"AC1"},
		QualityGaps:    []string{"quality-a"},
		Findings:       []Finding{{Category: "quality-a", Summary: "quality a regressed"}},
	})

	assertReviewPassIDs(t, loaded.ReviewProgress.Acceptance, []string{"AC2"})
	assertReviewPassIDs(t, loaded.ReviewProgress.Quality, []string{"quality-b"})
	if loaded.AcceptanceCriteria[0].Status != "not_satisfied" {
		t.Fatalf("AC1 status = %q", loaded.AcceptanceCriteria[0].Status)
	}
}

func TestApplyReviewProgressTracksEveryQualityGapForOneConsolidatedFinding(t *testing.T) {
	t.Parallel()
	quality := &profile.Quality{ReviewDimensions: []profile.ReviewDimension{
		{ID: "regression", Pass: "no regressions"},
		{ID: "cross-platform", Pass: "works across supported platforms"},
	}}
	loaded := task.Task{}

	ApplyReviewProgress(&loaded, profile.Bundle{Quality: quality}, Verdict{
		QualityGaps: []string{"regression", "cross-platform"},
		Findings:    []Finding{{Category: "regression", Summary: "one path defect affects regression and cross-platform behavior"}},
	})

	assertReviewPassIDs(t, loaded.ReviewProgress.Quality, nil)
}

func TestApplyReviewProgressUsesQualityGapsAsOnlyFailureSet(t *testing.T) {
	t.Parallel()
	quality := &profile.Quality{ReviewDimensions: []profile.ReviewDimension{{ID: "quality-a", Pass: "quality a passes"}}}
	loaded := task.Task{}

	ApplyReviewProgress(&loaded, profile.Bundle{Quality: quality}, Verdict{
		QualityPasses: []string{"quality-a"},
		QualityGaps:   []string{},
		Findings:      []Finding{{Severity: "low", Category: "quality-a", Summary: "non-blocking observation"}},
	})

	assertReviewPassIDs(t, loaded.ReviewProgress.Quality, []string{"quality-a"})
}

func TestApplyReviewProgressDoesNotPromoteQualityWithoutOutcomeList(t *testing.T) {
	t.Parallel()
	quality := &profile.Quality{ReviewDimensions: []profile.ReviewDimension{{ID: "regression", Pass: "no regressions"}}}
	loaded := task.Task{}

	ApplyReviewProgress(&loaded, profile.Bundle{Quality: quality}, Verdict{})

	assertReviewPassIDs(t, loaded.ReviewProgress.Quality, nil)
}

func TestApplyReviewProgressSkipsOnlyMalformedAcceptanceResults(t *testing.T) {
	t.Parallel()
	quality := &profile.Quality{ReviewDimensions: []profile.ReviewDimension{{ID: "quality-a", Pass: "quality a passes"}}}
	loaded := task.Task{AcceptanceCriteria: []task.AcceptanceCriterion{{ID: "AC1", Text: "first", Verification: "verify first"}}}

	ApplyReviewProgress(&loaded, profile.Bundle{Quality: quality}, Verdict{
		AcceptanceEvidence: []AcceptanceEvidence{{ACID: "AC1", Evidence: []string{"claimed pass"}}},
		AcceptanceGaps:     []string{"AC1 still fails"},
		QualityPasses:      []string{"quality-a"},
		QualityGaps:        []string{},
	})

	assertReviewPassIDs(t, loaded.ReviewProgress.Acceptance, nil)
	assertReviewPassIDs(t, loaded.ReviewProgress.Quality, []string{"quality-a"})
	if loaded.AcceptanceCriteria[0].Status != "pending" {
		t.Fatalf("AC1 status = %q, want pending", loaded.AcceptanceCriteria[0].Status)
	}
}

func TestApplyReviewProgressSkipsOnlyMalformedQualityResults(t *testing.T) {
	t.Parallel()
	quality := &profile.Quality{ReviewDimensions: []profile.ReviewDimension{{ID: "quality-a", Pass: "quality a passes"}}}
	loaded := task.Task{AcceptanceCriteria: []task.AcceptanceCriterion{{ID: "AC1", Text: "first", Verification: "verify first"}}}

	ApplyReviewProgress(&loaded, profile.Bundle{Quality: quality}, Verdict{
		AcceptanceEvidence: []AcceptanceEvidence{{ACID: "AC1", Evidence: []string{"pass evidence"}}},
		QualityPasses:      []string{"unknown"},
		QualityGaps:        []string{},
	})

	assertReviewPassIDs(t, loaded.ReviewProgress.Acceptance, []string{"AC1"})
	assertReviewPassIDs(t, loaded.ReviewProgress.Quality, nil)
}

func TestApplyReviewProgressAppliesKnownGapsFromMalformedResults(t *testing.T) {
	t.Parallel()
	quality := &profile.Quality{ReviewDimensions: []profile.ReviewDimension{{ID: "quality-a", Pass: "quality a passes"}}}
	loaded := task.Task{AcceptanceCriteria: []task.AcceptanceCriterion{{ID: "AC1", Text: "first", Verification: "verify first"}}}
	ApplyReviewProgress(&loaded, profile.Bundle{Quality: quality}, Verdict{
		AcceptanceEvidence: []AcceptanceEvidence{{ACID: "AC1", Evidence: []string{"initial pass"}}},
		QualityPasses:      []string{"quality-a"},
		QualityGaps:        []string{},
	})

	ApplyReviewProgress(&loaded, profile.Bundle{Quality: quality}, Verdict{
		AcceptanceEvidence: []AcceptanceEvidence{{ACID: "AC1", Evidence: []string{"contradictory pass"}}},
		AcceptanceGaps:     []string{"AC1"},
		QualityPasses:      []string{"unknown"},
		QualityGaps:        []string{"quality-a"},
	})

	assertReviewPassIDs(t, loaded.ReviewProgress.Acceptance, nil)
	assertReviewPassIDs(t, loaded.ReviewProgress.Quality, nil)
	if loaded.AcceptanceCriteria[0].Status != "not_satisfied" {
		t.Fatalf("AC1 status = %q, want not_satisfied", loaded.AcceptanceCriteria[0].Status)
	}
}

func TestReconcileReviewProgressResetsWhenReviewContractChanges(t *testing.T) {
	profiles := profile.Bundle{Quality: &profile.Quality{ReviewDimensions: []profile.ReviewDimension{{ID: "quality-a", Pass: "original"}}}}
	loaded := task.Task{AcceptanceCriteria: []task.AcceptanceCriterion{{ID: "AC1", Text: "original", Verification: "verify"}}}
	ApplyReviewProgress(&loaded, profiles, Verdict{
		AcceptanceEvidence: []AcceptanceEvidence{{ACID: "AC1", Evidence: []string{"evidence"}}},
		QualityPasses:      []string{"quality-a"},
		QualityGaps:        []string{},
	})

	loaded.AcceptanceCriteria[0].Text = "changed"
	ReconcileReviewProgress(&loaded, profiles)

	if loaded.ReviewProgress == nil {
		t.Fatal("review progress is nil")
	}
	if len(loaded.ReviewProgress.Acceptance) != 0 || len(loaded.ReviewProgress.Quality) != 0 {
		t.Fatalf("stale passes retained: %#v", loaded.ReviewProgress)
	}
	if loaded.AcceptanceCriteria[0].Status != "pending" {
		t.Fatalf("AC1 status = %q", loaded.AcceptanceCriteria[0].Status)
	}
}

func TestReconcileReviewProgressDropsPassesWhenQualityContractChanges(t *testing.T) {
	t.Parallel()
	profiles := profile.Bundle{Quality: &profile.Quality{
		ReviewDimensions: []profile.ReviewDimension{{ID: "quality-a", Pass: "original"}},
	}}
	loaded := task.Task{AcceptanceCriteria: []task.AcceptanceCriterion{{ID: "AC1", Text: "original", Verification: "verify"}}}
	ApplyReviewProgress(&loaded, profiles, Verdict{
		AcceptanceEvidence: []AcceptanceEvidence{{ACID: "AC1", Evidence: []string{"evidence"}}},
		QualityPasses:      []string{"quality-a"},
		QualityGaps:        []string{},
	})

	profiles.Quality.ReviewDimensions[0].Pass = "changed"
	ReconcileReviewProgress(&loaded, profiles)

	if len(loaded.ReviewProgress.Acceptance) != 0 || len(loaded.ReviewProgress.Quality) != 0 {
		t.Fatalf("passes retained after quality contract change: %#v", loaded.ReviewProgress)
	}
}

func TestReconcileReviewProgressDropsPassesWhenReviewInputsChange(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*task.Task, *profile.Bundle)
	}{
		{
			name: "task file",
			mutate: func(value *task.Task, _ *profile.Bundle) {
				value.Files[0].Description = "changed requirement"
			},
		},
		{
			name: "preflight obligation",
			mutate: func(value *task.Task, _ *profile.Bundle) {
				value.Preflight.AcceptanceSkeleton.Outputs[0].Purpose = "changed purpose"
			},
		},
		{
			name: "environment constraint",
			mutate: func(_ *task.Task, profiles *profile.Bundle) {
				profiles.Environment.Constraints.Network = "denied"
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			loaded := task.Task{
				AcceptanceCriteria: []task.AcceptanceCriterion{{ID: "AC1", Text: "original", Verification: "verify"}},
				Files: []task.InputFile{{
					Source: "requirements.md", Destination: "docs/requirements.md", Description: "requirement", Commit: false,
				}},
				Preflight: &task.Preflight{AcceptanceSkeleton: &task.AcceptanceSkeletonConfig{
					Enabled: true,
					Outputs: []task.AcceptanceSkeletonOutputDef{{ACID: "AC1", Path: "ac1_test.go", Purpose: "verify AC1", ImplementationRequired: true}},
				}},
			}
			profiles := profile.Bundle{Environment: &profile.Environment{
				Commands:    map[string]string{"tests": "go test ./..."},
				Constraints: profile.Constraints{Network: "allowed"},
			}}
			ApplyReviewProgress(&loaded, profiles, Verdict{
				AcceptanceEvidence: []AcceptanceEvidence{{ACID: "AC1", Evidence: []string{"evidence"}}},
			})

			tt.mutate(&loaded, &profiles)
			ReconcileReviewProgress(&loaded, profiles)

			if len(loaded.ReviewProgress.Acceptance) != 0 {
				t.Fatalf("passes retained after %s change: %#v", tt.name, loaded.ReviewProgress)
			}
		})
	}
}

func TestApplyReviewProgressAddressesPassedRevisionRequestsFromAnySource(t *testing.T) {
	t.Parallel()
	loaded := task.Task{
		RevisionRequests: []task.RevisionRequest{
			{ID: "pr-comment-1", Source: "pr-comment", Status: "pending"},
			{ID: "manual-1", Source: "manual", Status: "pending"},
		},
	}

	ApplyReviewProgress(&loaded, profile.Bundle{}, Verdict{AcceptanceEvidence: []AcceptanceEvidence{
		{ACID: "revision:pr-comment-1", Evidence: []string{"fixed in loop.go", "covered by regression test"}},
		{ACID: "revision:manual-1", Evidence: []string{"fixed in task.go"}},
	}})

	if request := loaded.RevisionRequests[0]; request.Status != "addressed" || request.Evidence != "fixed in loop.go; covered by regression test" {
		t.Fatalf("passed revision request = %#v", request)
	}
	if request := loaded.RevisionRequests[1]; request.Status != "addressed" || request.Evidence != "fixed in task.go" {
		t.Fatalf("passed manual revision request = %#v", request)
	}
}

func TestApplyReviewProgressKeepsContradictoryRevisionPending(t *testing.T) {
	t.Parallel()
	loaded := task.Task{RevisionRequests: []task.RevisionRequest{{ID: "review-1", Source: "supervisor", Status: "pending"}}}

	ApplyReviewProgress(&loaded, profile.Bundle{}, Verdict{
		AcceptanceEvidence: []AcceptanceEvidence{{ACID: "revision:review-1", Evidence: []string{"claimed fixed"}}},
		AcceptanceGaps:     []string{"revision:review-1"},
	})

	if request := loaded.RevisionRequests[0]; request.Status != "pending" || request.Evidence != "" {
		t.Fatalf("contradictory revision result must remain pending: %#v", request)
	}
}

func TestReconcileReviewProgressKeepsPassesForOperationalChanges(t *testing.T) {
	t.Parallel()
	loaded := task.Task{
		AcceptanceCriteria: []task.AcceptanceCriterion{{ID: "AC1", Text: "original", Verification: "verify"}},
		Scope:              task.Scope{AllowedPaths: []string{"internal/"}},
	}
	profiles := profile.Bundle{Environment: &profile.Environment{
		Commands: map[string]string{"lint": "go vet ./...", "tests": "go test ./..."},
		Executor: &profile.ExecutorDefault{DefaultCLI: "claude", Model: "model-a", Effort: "high"},
	}}
	ApplyReviewProgress(&loaded, profiles, Verdict{
		AcceptanceEvidence: []AcceptanceEvidence{{ACID: "AC1", Evidence: []string{"evidence"}}},
	})

	profiles.Environment.Executor.Model = "model-b"
	profiles.Environment.Executor.Effort = "medium"
	profiles.Environment.Commands = map[string]string{"tests": "go test ./internal/..."}
	ReconcileReviewProgress(&loaded, profiles)

	assertReviewPassIDs(t, loaded.ReviewProgress.Acceptance, []string{"AC1"})
}

func TestReconcileReviewProgressDropsPassesWhenTaskDirectionChanges(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*task.Task)
	}{
		{name: "goal", mutate: func(value *task.Task) { value.Goal = "changed goal" }},
		{name: "cwd", mutate: func(value *task.Task) { value.Scope.CWD = "/repo/changed" }},
		{name: "allowed paths", mutate: func(value *task.Task) { value.Scope.AllowedPaths = []string{"docs/"} }},
		{name: "forbidden paths", mutate: func(value *task.Task) { value.Scope.ForbiddenPaths = []string{"internal/private/"} }},
		{name: "permission", mutate: func(value *task.Task) { value.Scope.Permission = "read" }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			loaded := task.Task{
				Goal:               "original goal",
				AcceptanceCriteria: []task.AcceptanceCriterion{{ID: "AC1", Text: "original", Verification: "verify"}},
				Scope:              task.Scope{CWD: "/repo/original", AllowedPaths: []string{"internal/"}, ForbiddenPaths: []string{".env"}, Permission: "edit"},
			}
			ApplyReviewProgress(&loaded, profile.Bundle{}, Verdict{
				AcceptanceEvidence: []AcceptanceEvidence{{ACID: "AC1", Evidence: []string{"evidence"}}},
			})

			tt.mutate(&loaded)
			ReconcileReviewProgress(&loaded, profile.Bundle{})

			assertReviewPassIDs(t, loaded.ReviewProgress.Acceptance, nil)
		})
	}
}

func TestReconcileReviewProgressDropsPassesWhenPlacedInputContentChanges(t *testing.T) {
	t.Parallel()
	loaded := task.Task{AcceptanceCriteria: []task.AcceptanceCriterion{{ID: "AC1", Text: "original", Verification: "verify"}}}
	initial := ReviewContractContext{InputFilesDigest: "digest-v1"}
	ApplyReviewProgressWithContext(&loaded, profile.Bundle{}, initial, Verdict{
		AcceptanceEvidence: []AcceptanceEvidence{{ACID: "AC1", Evidence: []string{"evidence"}}},
	})

	ReconcileReviewProgressWithContext(&loaded, profile.Bundle{}, ReviewContractContext{InputFilesDigest: "digest-v2"})

	assertReviewPassIDs(t, loaded.ReviewProgress.Acceptance, nil)
}

func TestReviewContractUsesSourceCWDForEffectiveWorktreeTask(t *testing.T) {
	t.Parallel()
	loaded := task.Task{
		AcceptanceCriteria: []task.AcceptanceCriterion{{ID: "AC1", Text: "original", Verification: "verify"}},
		Scope:              task.Scope{CWD: "/repo/source"},
	}
	context := ReviewContractContext{SourceCWD: "/repo/source"}
	ApplyReviewProgressWithContext(&loaded, profile.Bundle{}, context, Verdict{
		AcceptanceEvidence: []AcceptanceEvidence{{ACID: "AC1", Evidence: []string{"evidence"}}},
	})

	effective := loaded
	effective.Scope.CWD = "/repo/worktree"
	ReconcileReviewProgressWithContext(&effective, profile.Bundle{}, context)

	assertReviewPassIDs(t, effective.ReviewProgress.Acceptance, []string{"AC1"})
}

func assertReviewPassIDs(t *testing.T, passes []string, want []string) {
	t.Helper()
	if len(passes) != len(want) {
		t.Fatalf("passes = %#v, want IDs %v", passes, want)
	}
	for i := range want {
		if passes[i] != want[i] {
			t.Fatalf("passes[%d] = %#v, want ID %q", i, passes[i], want[i])
		}
	}
}
