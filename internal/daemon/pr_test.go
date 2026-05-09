package daemon

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/shinpr/galley/internal/task"
	"github.com/shinpr/galley/internal/workspace"
)

func TestPRTitleTruncatesRunes(t *testing.T) {
	t.Parallel()
	title := prTitle(task.Task{Goal: strings.Repeat("界", 80)})
	if len([]rune(title)) != 72 {
		t.Fatalf("rune length got %d", len([]rune(title)))
	}
	if !utf8.ValidString(title) {
		t.Fatalf("title is invalid UTF-8: %q", title)
	}
}

func TestRenderPRBodyOmitsResolvedAttemptRisks(t *testing.T) {
	t.Parallel()
	body := renderPRBody(task.Task{
		ID:   "T1",
		Goal: "Ship it",
		Risks: []task.Risk{
			{ID: "claude-risk-1", Type: "partial_verification", Detail: "old test failed", Mitigation: "rerun"},
			{ID: "workspace-dirty-2", Type: "technical_debt", Detail: "old dirty tree", Mitigation: "recorded"},
			{ID: "security-1", Type: "security", Detail: "manual review still needed", Mitigation: "review"},
		},
	}, "run-1")
	if strings.Contains(body, "old test failed") || strings.Contains(body, "old dirty tree") {
		t.Fatalf("PR body leaked resolved attempt risks:\n%s", body)
	}
	if !strings.Contains(body, "manual review still needed") {
		t.Fatalf("PR body missing active risk:\n%s", body)
	}
}

func TestRenderPRBodyIncludesDecisionRationale(t *testing.T) {
	t.Parallel()
	body := renderPRBody(task.Task{
		ID:   "T1",
		Goal: "Ship it",
		Decisions: []task.Decision{{
			ID:               "claude-decision-1",
			Question:         "Which API shape should metadata filters use?",
			Chosen:           "Record<string,string>",
			Rationale:        "Matches CLI key=value flags and MCP object schema.",
			Reversibility:    "high",
			NeedsHumanReview: true,
		}},
	}, "run-1")
	for _, want := range []string{
		"Record<string,string>",
		"Matches CLI key=value flags",
		"Reversibility: high",
		"Human review suggested: true",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("PR body missing %q:\n%s", want, body)
		}
	}
}

func TestEnsureNonCommittedInputsAbsentUsesChangedFiles(t *testing.T) {
	t.Parallel()
	files := []task.InputFile{{Destination: "docs/plan.md", Commit: false}}
	snapshot := workspace.Snapshot{
		BranchDiff:  "code contains b/docs/plan.md as text",
		BranchFiles: []string{"docs/plan.md.bak"},
	}
	if err := ensureNonCommittedInputsAbsentFromBranch(snapshot, files); err != nil {
		t.Fatalf("substring-only diff text should not fail: %v", err)
	}
	snapshot.BranchFiles = []string{"docs/plan.md"}
	if err := ensureNonCommittedInputsAbsentFromBranch(snapshot, files); err == nil {
		t.Fatal("expected committed non-committed input file error")
	}
}
