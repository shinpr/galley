package supervisor

import (
	"errors"
	"strings"
	"testing"

	"github.com/shinpr/galley/internal/profile"
	"github.com/shinpr/galley/internal/runner"
	"github.com/shinpr/galley/internal/task"
)

func TestEvaluateAcceptsCompletedWithDiff(t *testing.T) {
	verdict := Evaluate(Evidence{
		Task: task.Task{AcceptanceCriteria: []task.AcceptanceCriterion{
			{ID: "AC1", Text: "Change is implemented."},
		}},
		Claude: runner.ClaudeResult{
			Status:             "completed",
			Summary:            "done",
			FilesModified:      []string{"file.go"},
			AcceptanceCriteria: []runner.ClaudeAcceptanceCriterion{{ID: "AC1", Status: "satisfied", Evidence: []string{"diff"}}},
			Verification:       []runner.ClaudeVerification{},
			Decisions:          []runner.ClaudeDecision{},
			Risks:              []runner.ClaudeRisk{},
		},
		DiffDirty: true,
	})
	if verdict.Status != "accepted" {
		t.Fatalf("status got %q", verdict.Status)
	}
}

func TestEvaluateRequiresEveryTaskAcceptanceCriterion(t *testing.T) {
	verdict := Evaluate(Evidence{
		Task: task.Task{AcceptanceCriteria: []task.AcceptanceCriterion{
			{ID: "AC1", Text: "Change is implemented."},
		}},
		Claude: runner.ClaudeResult{
			Status:             "completed",
			Summary:            "done",
			FilesModified:      []string{"file.go"},
			AcceptanceCriteria: []runner.ClaudeAcceptanceCriterion{},
			Verification:       []runner.ClaudeVerification{},
			Decisions:          []runner.ClaudeDecision{},
			Risks:              []runner.ClaudeRisk{},
		},
		DiffDirty:    true,
		AttemptsLeft: 1,
	})
	if verdict.Status != "needs_revision" {
		t.Fatalf("status got %q", verdict.Status)
	}
	if len(verdict.AcceptanceGaps) == 0 || !strings.Contains(verdict.AcceptanceGaps[0], "AC1") {
		t.Fatalf("acceptance gaps got %#v", verdict.AcceptanceGaps)
	}
	if !strings.Contains(verdict.NextWorkOrder, "Acceptance Gaps") {
		t.Fatalf("next work order should include acceptance gaps: %q", verdict.NextWorkOrder)
	}
}

func TestEvaluateRequiresQualityProfileChecks(t *testing.T) {
	verdict := Evaluate(Evidence{
		Task: task.Task{AcceptanceCriteria: []task.AcceptanceCriterion{
			{ID: "AC1", Text: "Change is implemented."},
		}},
		Profiles: profile.Bundle{Quality: &profile.Quality{RequiredChecks: []profile.RequiredCheck{
			{ID: "unit", PreferredCommands: []string{"go test ./..."}, Required: true},
		}}},
		Claude: runner.ClaudeResult{
			Status:             "completed",
			Summary:            "done",
			FilesModified:      []string{"file.go"},
			AcceptanceCriteria: []runner.ClaudeAcceptanceCriterion{{ID: "AC1", Status: "satisfied", Evidence: []string{"diff"}}},
			Verification:       []runner.ClaudeVerification{},
			Decisions:          []runner.ClaudeDecision{},
			Risks:              []runner.ClaudeRisk{},
		},
		DiffDirty:    true,
		AttemptsLeft: 1,
	})
	if verdict.Status != "needs_revision" {
		t.Fatalf("status got %q", verdict.Status)
	}
	if len(verdict.AcceptanceGaps) == 0 || !strings.Contains(verdict.AcceptanceGaps[0], "required quality check unit") {
		t.Fatalf("acceptance gaps got %#v", verdict.AcceptanceGaps)
	}
}

func TestEvaluateAcceptsPassedQualityProfileChecks(t *testing.T) {
	verdict := Evaluate(Evidence{
		Task: task.Task{AcceptanceCriteria: []task.AcceptanceCriterion{
			{ID: "AC1", Text: "Change is implemented."},
		}},
		Profiles: profile.Bundle{Quality: &profile.Quality{RequiredChecks: []profile.RequiredCheck{
			{ID: "unit", PreferredCommands: []string{"go test ./..."}, Required: true},
		}}},
		Claude: runner.ClaudeResult{
			Status:             "completed",
			Summary:            "done",
			FilesModified:      []string{"file.go"},
			AcceptanceCriteria: []runner.ClaudeAcceptanceCriterion{{ID: "AC1", Status: "satisfied", Evidence: []string{"diff"}}},
			Verification:       []runner.ClaudeVerification{{Command: "go test ./...", Status: "passed"}},
			Decisions:          []runner.ClaudeDecision{},
			Risks:              []runner.ClaudeRisk{},
		},
		DiffDirty: true,
	})
	if verdict.Status != "accepted" {
		t.Fatalf("status got %q: %#v", verdict.Status, verdict)
	}
}

func TestEvaluateRequiresPendingRevisionRequests(t *testing.T) {
	verdict := Evaluate(Evidence{
		Task: task.Task{
			AcceptanceCriteria: []task.AcceptanceCriterion{{ID: "AC1", Text: "Change is implemented."}},
			RevisionRequests: []task.RevisionRequest{{
				ID:     "pr-comment-42",
				Source: "pr_comment",
				Text:   "Change the help text.",
				Status: "pending",
			}},
		},
		Claude: runner.ClaudeResult{
			Status:             "completed",
			Summary:            "done",
			FilesModified:      []string{"file.go"},
			AcceptanceCriteria: []runner.ClaudeAcceptanceCriterion{{ID: "AC1", Status: "satisfied", Evidence: []string{"diff"}}},
			Verification:       []runner.ClaudeVerification{},
			Decisions:          []runner.ClaudeDecision{},
			Risks:              []runner.ClaudeRisk{},
		},
		DiffDirty:    true,
		AttemptsLeft: 1,
	})
	if verdict.Status != "needs_revision" {
		t.Fatalf("status got %q", verdict.Status)
	}
	if len(verdict.AcceptanceGaps) == 0 || !strings.Contains(strings.Join(verdict.AcceptanceGaps, "\n"), "pr-comment-42") {
		t.Fatalf("acceptance gaps got %#v", verdict.AcceptanceGaps)
	}
	if !strings.Contains(verdict.NextWorkOrder, "Pending Revision Requests") {
		t.Fatalf("next work order should include revision requests: %q", verdict.NextWorkOrder)
	}
}

func TestEvaluateRequestsRevisionWhenAttemptsRemain(t *testing.T) {
	verdict := Evaluate(Evidence{
		Task:         task.Task{ID: "T1", Goal: "Fix it"},
		ParseError:   errors.New("bad json"),
		AttemptsLeft: 1,
	})
	if verdict.Status != "needs_revision" {
		t.Fatalf("status got %q", verdict.Status)
	}
	if !strings.Contains(verdict.NextWorkOrder, "Corrective Work Order") {
		t.Fatalf("missing corrective work order: %q", verdict.NextWorkOrder)
	}
}

func TestEvaluateHardStopStopsOnExternalBlocker(t *testing.T) {
	verdict := Evaluate(Evidence{
		Claude: runner.ClaudeResult{
			Status:             "hard_stop",
			Summary:            "blocked",
			FilesModified:      []string{},
			AcceptanceCriteria: []runner.ClaudeAcceptanceCriterion{},
			Verification:       []runner.ClaudeVerification{},
			Decisions:          []runner.ClaudeDecision{},
			Risks:              []runner.ClaudeRisk{},
			HardStop:           &runner.ClaudeHardStop{Reason: "missing secret"},
		},
		AttemptsLeft: 1,
	})
	if verdict.Status != "hard_stop" {
		t.Fatalf("status got %q", verdict.Status)
	}
}

func TestEvaluateHardStopRequestsRevisionWhenRetryable(t *testing.T) {
	verdict := Evaluate(Evidence{
		Task: task.Task{ID: "T1", Goal: "Implement the feature"},
		Claude: runner.ClaudeResult{
			Status:             "hard_stop",
			Summary:            "blocked",
			FilesModified:      []string{},
			AcceptanceCriteria: []runner.ClaudeAcceptanceCriterion{},
			Verification:       []runner.ClaudeVerification{},
			Decisions:          []runner.ClaudeDecision{},
			Risks:              []runner.ClaudeRisk{},
			HardStop: &runner.ClaudeHardStop{
				Reason:           "This seems outside my role.",
				Attempted:        []string{"read the task"},
				NeededToContinue: []string{"a clearer implementation path"},
			},
		},
		AttemptsLeft: 1,
	})
	if verdict.Status != "needs_revision" {
		t.Fatalf("status got %q", verdict.Status)
	}
	if !strings.Contains(verdict.NextWorkOrder, "executor returned hard_stop") {
		t.Fatalf("next work order missing hard stop reason: %q", verdict.NextWorkOrder)
	}
	if !strings.Contains(verdict.NextWorkOrder, "a clearer implementation path") {
		t.Fatalf("next work order missing unblock claim: %q", verdict.NextWorkOrder)
	}
}
