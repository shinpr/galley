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
	loaded.Risks = append(loaded.Risks, Risk{
		ID:     "requeue-supervisor-attempt-1-guidance",
		Detail: "Preserve the existing contract while closing the publication boundary.",
	})
	loaded.RevisionRequests = append(loaded.RevisionRequests, RevisionRequest{
		ID:        "pr-comment-42",
		Source:    "pr_comment",
		CommentID: "42",
		Text:      "Please rename the proof file and update the README.",
		Status:    "pending",
	})
	loaded.RevisionRequests = append(loaded.RevisionRequests, RevisionRequest{
		ID:     "supervisor-attempt-1-finding-1",
		Source: "supervisor",
		Text:   "An earlier request that is already complete.",
		Status: "addressed",
	})
	loaded.ReviewProgress = &ReviewProgress{
		Acceptance: []string{"AC1"},
		Quality:    []string{"regression"},
	}

	workOrder := RenderWorkOrder(loaded)
	for _, want := range []string{
		"https://github.com/shinpr/sandbox/pull/3",
		"review iteration: `2`",
		"Preserve the existing contract while closing the publication boundary.",
		"acceptance: `AC1`",
		"quality: `regression`",
		"`acceptance_criteria`",
		"`revision:pr-comment-42`",
		"Please rename the proof file and update the README.",
	} {
		if !strings.Contains(workOrder, want) {
			t.Fatalf("work order missing %q:\n%s", want, workOrder)
		}
	}
	if strings.Contains(workOrder, "An earlier request that is already complete.") {
		t.Fatalf("work order contains addressed revision request:\n%s", workOrder)
	}
	goalIndex := strings.Index(workOrder, loaded.Goal)
	for _, active := range []string{
		"Preserve the existing contract while closing the publication boundary.",
		"Please rename the proof file and update the README.",
		"acceptance: `AC1`",
	} {
		if strings.Index(workOrder, active) >= goalIndex {
			t.Fatalf("active revision context %q does not precede the task goal:\n%s", active, workOrder)
		}
	}
	if acceptanceIndex := strings.Index(workOrder, loaded.AcceptanceCriteria[0].Text); acceptanceIndex <= goalIndex {
		t.Fatalf("acceptance criteria do not follow the task goal:\n%s", workOrder)
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
