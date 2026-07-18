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
			Executor: &profile.ExecutorDefault{DefaultCLI: "codex"},
			Constraints: profile.Constraints{
				Network:             "approval_required",
				SecretsPolicy:       "never_read_env_files",
				DestructiveCommands: "deny",
			},
		},
	})
	for _, want := range []string{"## Quality Profile", "go test ./...", "## Environment Profile", "executor default: `codex`", "never_read_env_files"} {
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
		"`revision:pr-comment-42`",
		"Please rename the proof file and update the README.",
	} {
		if !strings.Contains(workOrder, want) {
			t.Fatalf("work order missing %q:\n%s", want, workOrder)
		}
	}
}

func TestRenderWorkOrderIncludesInputFiles(t *testing.T) {
	t.Parallel()
	loaded, err := Load(writeTaskYAML(t, "loop_budget: 3"))
	if err != nil {
		t.Fatal(err)
	}
	loaded.Files = []InputFile{{
		Source:      "/tmp/plan.md",
		Destination: ".galley/inputs/plan.md",
		Description: "Implementation plan",
		Commit:      false,
	}}

	workOrder := RenderWorkOrder(loaded)
	for _, want := range []string{
		"## Input Files",
		".galley/inputs/plan.md",
		"Implementation plan",
		"commit with task changes: `false`",
		"Galley removes this file before commit/PR finalization",
	} {
		if !strings.Contains(workOrder, want) {
			t.Fatalf("work order missing %q:\n%s", want, workOrder)
		}
	}
}
