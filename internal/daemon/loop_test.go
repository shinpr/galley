package daemon

import (
	"testing"

	"github.com/shinpr/galley/internal/runner"
)

func TestMergeExecutorJudgmentPreservesSemanticAcceptance(t *testing.T) {
	t.Parallel()
	generated := runner.ClaudeResult{
		Status:        "completed",
		Summary:       "generated",
		FilesModified: []string{"file.go"},
		AcceptanceCriteria: []runner.ClaudeAcceptanceCriterion{{
			ID:       "AC1",
			Status:   "not_satisfied",
			Evidence: []string{},
			Notes:    "guidance only",
		}},
		Verification: []runner.ClaudeVerification{{
			Command: "go test ./...",
			Status:  "passed",
			Reason:  "ok",
		}},
		Decisions: []runner.ClaudeDecision{},
		Risks:     []runner.ClaudeRisk{},
	}
	reported := runner.ClaudeResult{
		Status:        "completed",
		Summary:       "reported",
		FilesModified: []string{"file.go"},
		AcceptanceCriteria: []runner.ClaudeAcceptanceCriterion{{
			ID:       "AC1",
			Status:   "satisfied",
			Evidence: []string{"diff shows behavior"},
			Notes:    "implemented",
		}},
		Verification: []runner.ClaudeVerification{},
		ScopeExpansions: []runner.ClaudeScopeExpansion{{
			Path:              "outside",
			Reason:            "needed for revision",
			LinkedRequirement: "revision:pr-comment-1",
			Minimality:        "one directory",
		}},
		Decisions: []runner.ClaudeDecision{},
		Risks:     []runner.ClaudeRisk{},
	}

	merged := mergeExecutorJudgment(generated, reported)
	if merged.AcceptanceCriteria[0].Status != "satisfied" {
		t.Fatalf("acceptance status got %q", merged.AcceptanceCriteria[0].Status)
	}
	if len(merged.Verification) != 1 || merged.Verification[0].Command != "go test ./..." {
		t.Fatalf("verification got %#v", merged.Verification)
	}
	if len(merged.ScopeExpansions) != 1 || merged.ScopeExpansions[0].Path != "outside" {
		t.Fatalf("scope expansions got %#v", merged.ScopeExpansions)
	}
}

func TestExecutorVerificationCmdUnknownIsStable(t *testing.T) {
	t.Parallel()
	if got := executorVerificationCmd("opus-cli"); got != "unknown" {
		t.Fatalf("executorVerificationCmd unknown got %q, want unknown", got)
	}
}
