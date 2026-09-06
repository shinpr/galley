package supervisor

import (
	"testing"

	"github.com/shinpr/galley/internal/profile"
	"github.com/shinpr/galley/internal/task"
)

func TestApplyReviewProgressReplacesPassLists(t *testing.T) {
	quality := &profile.Quality{ReviewDimensions: []profile.ReviewDimension{
		{ID: "quality-a", Pass: "quality a passes"},
		{ID: "quality-b", Pass: "quality b passes"},
	}}
	loaded := task.Task{AcceptanceCriteria: []task.AcceptanceCriterion{
		{ID: "AC1", Text: "first", Verification: "verify first", Status: "pending"},
		{ID: "AC2", Text: "second", Verification: "verify second", Status: "pending"},
	}}

	ApplyReviewProgress(&loaded, profile.Bundle{Quality: quality}, Verdict{
		AcceptancePasses: []string{"AC1", "unknown"},
		QualityPasses:    []string{"quality-a", "unknown"},
	})

	assertReviewPassIDs(t, loaded.ReviewProgress.Acceptance, []string{"AC1"})
	assertReviewPassIDs(t, loaded.ReviewProgress.Quality, []string{"quality-a"})
	if loaded.AcceptanceCriteria[0].Status != "satisfied" || loaded.AcceptanceCriteria[1].Status != "pending" {
		t.Fatalf("acceptance statuses = %#v", loaded.AcceptanceCriteria)
	}

	ApplyReviewProgress(&loaded, profile.Bundle{Quality: quality}, Verdict{
		AcceptancePasses: []string{"AC2"},
		QualityPasses:    []string{"quality-b"},
	})

	assertReviewPassIDs(t, loaded.ReviewProgress.Acceptance, []string{"AC2"})
	assertReviewPassIDs(t, loaded.ReviewProgress.Quality, []string{"quality-b"})
	if loaded.AcceptanceCriteria[0].Status != "pending" || loaded.AcceptanceCriteria[1].Status != "satisfied" {
		t.Fatalf("acceptance statuses = %#v", loaded.AcceptanceCriteria)
	}
}

func TestReconcileReviewProgressResetsWhenReviewContractChanges(t *testing.T) {
	profiles := profile.Bundle{Quality: &profile.Quality{ReviewDimensions: []profile.ReviewDimension{{ID: "quality-a", Pass: "original"}}}}
	loaded := task.Task{AcceptanceCriteria: []task.AcceptanceCriterion{{ID: "AC1", Text: "original", Verification: "verify"}}}
	ApplyReviewProgress(&loaded, profiles, Verdict{
		AcceptancePasses: []string{"AC1"},
		QualityPasses:    []string{"quality-a"},
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
		AcceptancePasses: []string{"AC1"},
		QualityPasses:    []string{"quality-a"},
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
				AcceptancePasses: []string{"AC1"},
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

	ApplyReviewProgress(&loaded, profile.Bundle{}, Verdict{AcceptancePasses: []string{
		"revision:pr-comment-1",
		"revision:manual-1",
	}})

	if request := loaded.RevisionRequests[0]; request.Status != "addressed" || request.Evidence == "" {
		t.Fatalf("passed revision request = %#v", request)
	}
	if request := loaded.RevisionRequests[1]; request.Status != "addressed" || request.Evidence == "" {
		t.Fatalf("passed manual revision request = %#v", request)
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
		AcceptancePasses: []string{"AC1"},
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
				AcceptancePasses: []string{"AC1"},
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
		AcceptancePasses: []string{"AC1"},
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
		AcceptancePasses: []string{"AC1"},
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
