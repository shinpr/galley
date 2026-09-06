package daemon

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/shinpr/galley/internal/profile"
	"github.com/shinpr/galley/internal/supervisor"
	"github.com/shinpr/galley/internal/task"
	"github.com/shinpr/galley/internal/workspace"
)

func TestFinalVerificationShowsLatestFailure(t *testing.T) {
	commands := []task.VerificationCommand{{Cmd: "go test ./...", Status: "passed"}, {Cmd: "go test ./...", Status: "failed"}}
	got := finalVerificationCommands(commands)
	if len(got) != 1 || got[0].Status != "failed" {
		t.Fatalf("stale verification: %#v", got)
	}
}

func TestScopePathCasePolicies(t *testing.T) {
	t.Parallel()
	paths := []string{"Secrets/key.go"}
	if got := pathsInsideProtectedScope(paths, []string{"secrets"}); len(got) != 1 {
		t.Fatalf("protected matches got %v, want case-folded match", got)
	}
	if got := pathsOutsideScope(paths, []string{"secrets"}); len(got) != 1 {
		t.Fatalf("allowed-scope expansion got %v, want case-sensitive outside path", got)
	}
}

func TestPRTitleTruncatesRunes(t *testing.T) {
	t.Parallel()
	// Use single-byte ASCII so the byte-budget pass does not engage and we
	// can observe the rune-budget pass alone. A goal longer than the rune
	// budget with no whitespace exercises the no-boundary fallback inside the
	// rune cap; the result must be exactly prTitleRuneBudget runes and end
	// with the ellipsis marker.
	title := prTitle(task.Task{Goal: strings.Repeat("a", prTitleRuneBudget+8)})
	if got := len([]rune(title)); got != prTitleRuneBudget {
		t.Fatalf("rune length got %d, want %d", got, prTitleRuneBudget)
	}
	if !utf8.ValidString(title) {
		t.Fatalf("title is invalid UTF-8: %q", title)
	}
	if !strings.HasSuffix(title, prTitleEllipsis) {
		t.Fatalf("expected truncated title to end with ellipsis, got %q", title)
	}
}

// TestPRTitlePreservesOrdinaryLongASCIIGoal asserts that an ordinary long
// ASCII goal within the byte budget is preserved verbatim without truncation
// and never gains an ellipsis.
func TestPRTitlePreservesOrdinaryLongASCIIGoal(t *testing.T) {
	t.Parallel()
	// A representative ordinary long ASCII goal that fits within the byte budget without truncation.
	goal := "Restrict Galley PR comment commands so only the PR author, when also trusted by GitHub author association, can requeue or update a task."
	title := prTitle(task.Task{Goal: goal})
	if title != goal {
		t.Fatalf("ordinary long ASCII goal should stay untouched, got %q", title)
	}
	if strings.HasSuffix(title, prTitleEllipsis) {
		t.Fatalf("ordinary long ASCII goal should not gain ellipsis: %q", title)
	}
	if len(title) > prTitleByteBudget {
		t.Fatalf("title exceeds byte budget: bytes=%d title=%q", len(title), title)
	}
}

func TestPRTitleTruncatesOnWordBoundary(t *testing.T) {
	t.Parallel()

	t.Run("english long goal breaks on whitespace", func(t *testing.T) {
		assertEnglishLongGoalBreaksOnWhitespace(t)
	})

	t.Run("multibyte goal without whitespace falls back to rune cut", func(t *testing.T) {
		assertMultibyteGoalWithoutWhitespaceFallsBackToRuneCut(t)
	})

	t.Run("short goal stays untouched", func(t *testing.T) {
		assertShortGoalStaysUntouched(t)
	})

	// 4-byte UTF-8 runes such as emoji (🎉 = U+1F389, 4 bytes) can push a
	// rune-budgeted title above GitHub's 256-byte hard limit. Regression
	// guard for the byte budget enforcement: 200 emoji is 800 bytes, which
	// must be trimmed back to <= 256 bytes while staying valid UTF-8 and
	// keeping the ellipsis marker.
	t.Run("4-byte runes stay within byte budget", func(t *testing.T) {
		assert4ByteRunesStayWithinByteBudget(t)
	})

	// Mixed ASCII + 4-byte rune goal: GitHub's byte limit must hold even
	// when the goal carries enough emoji to push a 72-rune cut over 256
	// bytes. The byte pass should still prefer a whitespace boundary so
	// reviewers see complete words near the cut.
	t.Run("ascii plus 4-byte runes break on whitespace under byte budget", func(t *testing.T) {
		assertAsciiPlus4ByteRunesBreakOnWhitespaceUnderByteBudget(t)
	})
}

func assertEnglishLongGoalBreaksOnWhitespace(t *testing.T) {
	t.Helper()
	t.Parallel()
	// A goal past prTitleRuneBudget engages the rune-budget pass, whose cut
	// must land on a whitespace boundary inside the goal.
	goal := "Fix Galley user-visible output so that supervisor-accepted tasks no longer show stale not_satisfied AC status in the PR body, and also extend the executor work order rendering so reviewers can audit every acceptance criterion together with the recorded supervisor verdict, decision rationale, residual risks, and verification commands without opening the underlying run evidence directory."
	if len([]rune(goal)) <= prTitleRuneBudget {
		t.Fatalf("test setup invariant: goal must exceed rune budget; len=%d budget=%d", len([]rune(goal)), prTitleRuneBudget)
	}
	title := prTitle(task.Task{Goal: goal})
	if len([]rune(title)) > prTitleRuneBudget {
		t.Fatalf("title exceeds rune budget: len=%d title=%q", len([]rune(title)), title)
	}
	if !strings.HasSuffix(title, prTitleEllipsis) {
		t.Fatalf("expected ellipsis suffix, got %q", title)
	}
	// The character immediately before the ellipsis must not be a
	// whitespace and must come from a complete word in the original goal.
	core := strings.TrimSuffix(title, prTitleEllipsis)
	core = strings.TrimRight(core, " \t")
	if core == "" {
		t.Fatalf("title core is empty after trimming whitespace: %q", title)
	}
	// The core must end at a word boundary in the original goal — i.e.,
	// the next rune in the goal must be whitespace (or the goal must end).
	if !strings.HasPrefix(goal, core) {
		t.Fatalf("title core %q is not a prefix of goal", core)
	}
	next := goal[len(core):]
	if next != "" && next[0] != ' ' && next[0] != '\t' {
		t.Fatalf("title cut mid-word; next rune in goal is %q (title=%q)", string(next[0]), title)
	}
}

func assertMultibyteGoalWithoutWhitespaceFallsBackToRuneCut(t *testing.T) {
	t.Helper()
	t.Parallel()
	// A long no-whitespace multibyte goal exercises both passes; the result
	// must stay valid UTF-8, fit the byte budget, and end with the ellipsis.
	goal := strings.Repeat("界", prTitleRuneBudget+60)
	title := prTitle(task.Task{Goal: goal})
	if len(title) > prTitleByteBudget {
		t.Fatalf("title exceeds byte budget: bytes=%d title=%q", len(title), title)
	}
	if !utf8.ValidString(title) {
		t.Fatalf("title is invalid UTF-8: %q", title)
	}
	if !strings.HasSuffix(title, prTitleEllipsis) {
		t.Fatalf("expected ellipsis suffix on no-whitespace goal, got %q", title)
	}
	core := strings.TrimSuffix(title, prTitleEllipsis)
	for _, r := range core {
		if r != '界' {
			t.Fatalf("title core contains unexpected rune %q in %q", r, title)
		}
	}
}

func assertShortGoalStaysUntouched(t *testing.T) {
	t.Helper()
	t.Parallel()
	goal := "Tighten PR body status rendering"
	title := prTitle(task.Task{Goal: goal})
	if title != goal {
		t.Fatalf("short goal should stay untouched, got %q", title)
	}
	if strings.HasSuffix(title, prTitleEllipsis) {
		t.Fatalf("short goal should not gain ellipsis: %q", title)
	}
}

func assert4ByteRunesStayWithinByteBudget(t *testing.T) {
	t.Helper()
	t.Parallel()
	goal := strings.Repeat("🎉", 200)
	title := prTitle(task.Task{Goal: goal})
	if len(title) > prTitleByteBudget {
		t.Fatalf("title exceeds byte budget: bytes=%d title=%q", len(title), title)
	}
	if !utf8.ValidString(title) {
		t.Fatalf("title is invalid UTF-8: %q", title)
	}
	if !strings.HasSuffix(title, prTitleEllipsis) {
		t.Fatalf("expected ellipsis suffix on truncated 4-byte goal, got %q", title)
	}
	// The body before the ellipsis must consist entirely of complete
	// 🎉 runes — no partial 4-byte sequences sneaking through.
	core := strings.TrimSuffix(title, prTitleEllipsis)
	if !utf8.ValidString(core) {
		t.Fatalf("title core is invalid UTF-8: %q", core)
	}
	for _, r := range core {
		if r != '🎉' {
			t.Fatalf("title core contains unexpected rune %q in %q", r, title)
		}
	}
}

func assertAsciiPlus4ByteRunesBreakOnWhitespaceUnderByteBudget(t *testing.T) {
	t.Helper()
	t.Parallel()
	// 60 emoji * 4 bytes = 240 bytes, plus a long ASCII tail that
	// forces a break inside the byte budget.
	goal := strings.Repeat("🎉 ", 60) + "Tail context that should be dropped on truncation"
	title := prTitle(task.Task{Goal: goal})
	if len(title) > prTitleByteBudget {
		t.Fatalf("title exceeds byte budget: bytes=%d title=%q", len(title), title)
	}
	if !utf8.ValidString(title) {
		t.Fatalf("title is invalid UTF-8: %q", title)
	}
	if !strings.HasSuffix(title, prTitleEllipsis) {
		t.Fatalf("expected ellipsis suffix, got %q", title)
	}
	core := strings.TrimSuffix(title, prTitleEllipsis)
	if strings.HasSuffix(core, " ") {
		t.Fatalf("trailing whitespace before ellipsis: %q", title)
	}
}

func TestRenderPRBodyShowsSupervisorAcceptedStatus(t *testing.T) {
	t.Parallel()
	loaded := task.Task{
		ID:   "T1",
		Goal: "Ship it",
		AcceptanceCriteria: []task.AcceptanceCriterion{
			{ID: "AC1", Text: "Behavior", Verification: "go test ./...", Status: "pending"},
			{ID: "AC2", Text: "Docs", Verification: "grep docs", Status: "not_satisfied"},
		},
	}
	// Simulate the executor's earlier draft pending status moving through the
	// existing per-attempt merge code path.
	loaded.AcceptanceCriteria[0].Status = "not_satisfied"

	loaded.ReviewProgress = &task.ReviewProgress{Acceptance: []string{"AC1", "AC2"}}
	applyAcceptedAcceptanceCriteria(&loaded)

	body := renderPRBody(loaded)
	if !strings.Contains(body, "`AC1` Behavior") || !strings.Contains(body, "Status: satisfied") {
		t.Fatalf("expected satisfied AC status in PR body, got:\n%s", body)
	}
	if strings.Contains(body, "Status: pending") || strings.Contains(body, "Status: not_satisfied") {
		t.Fatalf("PR body still shows draft/stale AC status:\n%s", body)
	}
}

func TestApplyAcceptedAcceptanceCriteriaUsesPassList(t *testing.T) {
	t.Parallel()
	loaded := task.Task{
		AcceptanceCriteria: []task.AcceptanceCriterion{
			{ID: "AC1", Status: "not_satisfied"},
			{ID: "AC2", Status: "not_satisfied"},
		},
	}
	loaded.ReviewProgress = &task.ReviewProgress{Acceptance: []string{"AC1"}}
	applyAcceptedAcceptanceCriteria(&loaded)
	if loaded.AcceptanceCriteria[0].Status != "satisfied" {
		t.Fatalf("AC1 status got %q want satisfied", loaded.AcceptanceCriteria[0].Status)
	}
	if loaded.AcceptanceCriteria[1].Status != "partially_satisfied" {
		t.Fatalf("AC2 status got %q want partially_satisfied", loaded.AcceptanceCriteria[1].Status)
	}
}

func TestNewValidationEvidenceIncludesAuditableFields(t *testing.T) {
	t.Parallel()
	loaded := task.Task{ID: "task-evidence-1"}
	validation := task.ValidationResult{Task: loaded, Errors: []string{}, Warnings: []string{}}
	now := time.Date(2026, 5, 10, 12, 34, 56, 0, time.UTC)
	got := newValidationEvidence(loaded, validation, now)

	if !got.Valid {
		t.Fatalf("expected valid=true, got false")
	}
	if got.TaskID != "task-evidence-1" {
		t.Fatalf("task_id got %q", got.TaskID)
	}
	if got.SchemaVersion == "" {
		t.Fatalf("schema_version must be non-empty")
	}
	if got.GeneratedAt == "" || !strings.HasPrefix(got.GeneratedAt, "2026-05-10T12:34:56") {
		t.Fatalf("generated_at got %q", got.GeneratedAt)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"valid"`, `"task_id"`, `"schema_version"`, `"generated_at"`, `"errors"`, `"warnings"`, `"task"`} {
		if !strings.Contains(string(encoded), key) {
			t.Fatalf("encoded validation evidence missing %s: %s", key, encoded)
		}
	}
}

func TestNewValidationEvidenceReportsInvalidTasks(t *testing.T) {
	t.Parallel()
	loaded := task.Task{ID: "bad-task"}
	validation := task.ValidationResult{Task: loaded, Errors: []string{"missing goal"}}
	got := newValidationEvidence(loaded, validation, time.Now())
	if got.Valid {
		t.Fatalf("expected valid=false when validation has errors")
	}
	if len(got.Errors) != 1 || got.Errors[0] != "missing goal" {
		t.Fatalf("errors got %#v", got.Errors)
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
	})
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
	})
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

func TestRenderPRBodyIsReviewerFacing(t *testing.T) {
	t.Parallel()
	body := renderPRBody(task.Task{
		ID:   "T1",
		Goal: "Ship it",
		Verification: task.Verification{Commands: []task.VerificationCommand{
			{Cmd: "go test ./...", Status: "failed"},
			{Cmd: "go vet ./...", Status: "passed"},
			{Cmd: "go test ./...", Status: "passed"},
		}},
		DiscussionItems: []task.DiscussionItem{{
			ID:                    "discussion-1",
			Topic:                 "AC wording",
			Summary:               "The implementation satisfies the fixed AC, but the wording could be clearer for future tasks.",
			RequiresHumanDecision: true,
		}},
	})
	for _, forbidden := range []string{"Run Evidence", "run-1", "runs/"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("PR body leaked local evidence detail %q:\n%s", forbidden, body)
		}
	}
	if strings.Contains(body, "`go test ./...`: failed") {
		t.Fatalf("PR body included superseded verification result:\n%s", body)
	}
	for _, want := range []string{
		"## Final Verification",
		"`go vet ./...`: passed",
		"`go test ./...`: passed",
		"## Discussion Items",
		"Human decision required: true",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("PR body missing %q:\n%s", want, body)
		}
	}
}

func TestAcceptedPRShowsUnverifiedRevisionsWithoutEscalatingDiscussion(t *testing.T) {
	t.Parallel()
	loaded := task.Task{
		ID:   "T1",
		Goal: "Ship it",
		RevisionRequests: []task.RevisionRequest{
			{ID: "supervisor-open", Source: supervisorRevisionSource, Text: "fix the remaining edge case", Status: "pending"},
			{ID: "reviewer-open", Source: "pr-review", Text: "preserve the public behavior", Status: "pending"},
			{ID: "supervisor-done", Source: supervisorRevisionSource, Text: "already fixed", Status: "addressed"},
		},
	}
	mergeDiscussionItems(&loaded, profile.Bundle{}, supervisor.Verdict{
		Summary:         "Accepted with recorded context.",
		DiscussionItems: []string{"The fallback remains intentionally conservative."},
	})
	loaded = (supervisorRevision{}).applyToTask(loaded)

	if len(loaded.RevisionRequests) != 1 || loaded.RevisionRequests[0].ID != "reviewer-open" {
		t.Fatalf("historical supervisor revisions were not cleared: %#v", loaded.RevisionRequests)
	}
	unverified := 0
	for _, item := range loaded.DiscussionItems {
		if item.Topic == "Supervisor discussion" && item.RequiresHumanDecision {
			t.Fatalf("non-gating supervisor discussion requires a human decision: %#v", item)
		}
		if item.Topic == "Unverified revision request" {
			unverified++
			if !item.RequiresHumanDecision {
				t.Fatalf("unverified revision request does not require a human decision: %#v", item)
			}
		}
	}
	if unverified != 2 {
		t.Fatalf("unverified revision discussions got %d, want 2: %#v", unverified, loaded.DiscussionItems)
	}
	body := renderPRBody(loaded)
	for _, want := range []string{
		"revision:supervisor-open",
		"fix the remaining edge case",
		"revision:reviewer-open",
		"preserve the public behavior",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("PR body missing unverified revision %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "revision:supervisor-done") || strings.Contains(body, "already fixed") {
		t.Fatalf("PR body included addressed revision:\n%s", body)
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
