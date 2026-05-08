package task

import (
	"strings"
	"testing"

	"github.com/shinpr/galley/internal/profile"
)

func TestRenderWorkOrderWithProfiles(t *testing.T) {
	loaded, err := Load(writeTaskYAML(t, "loop_budget: 3"))
	if err != nil {
		t.Fatal(err)
	}
	workOrder := RenderWorkOrderWithProfiles(loaded, profile.Bundle{
		Quality: &profile.Quality{
			ID: "strict",
			RequiredChecks: []profile.RequiredCheck{
				{ID: "tests", PreferredCommands: []string{"go test ./..."}, Required: true},
			},
			PassPolicy: profile.PassPolicy{MinScore: 90},
		},
		Environment: &profile.Environment{
			ID:       "local",
			CWD:      loaded.Scope.CWD,
			Commands: map[string]string{"build": "go build ./cmd/galley"},
			Constraints: profile.Constraints{
				Network:             "approval_required",
				SecretsPolicy:       "never_read_env_files",
				DestructiveCommands: "deny",
			},
		},
	})
	for _, want := range []string{"## Quality Profile", "go test ./...", "## Environment Profile", "never_read_env_files"} {
		if !strings.Contains(workOrder, want) {
			t.Fatalf("work order missing %q:\n%s", want, workOrder)
		}
	}
}

func TestRenderWorkOrderIncludesPRReviewInstructions(t *testing.T) {
	t.Parallel()
	loaded, err := Load(writeTaskYAML(t, "loop_budget: 3"))
	if err != nil {
		t.Fatal(err)
	}
	loaded.PR.URL = "https://github.com/shinpr/sandbox/pull/3"
	loaded.Supervisor.ReviewIterations = 2
	loaded.RevisionRequests = append(loaded.RevisionRequests, RevisionRequest{
		ID:        "pr-comment-42",
		Source:    "pr_comment",
		CommentID: "42",
		Text:      "Please rename the proof file and update the README.",
		Status:    "pending",
	})

	workOrder := RenderWorkOrder(loaded)
	for _, want := range []string{
		"## PR Review Context",
		"## Revision Requests",
		"https://github.com/shinpr/sandbox/pull/3",
		"review iteration: `2`",
		"additional acceptance criterion",
		"Please rename the proof file and update the README.",
	} {
		if !strings.Contains(workOrder, want) {
			t.Fatalf("work order missing %q:\n%s", want, workOrder)
		}
	}
}
