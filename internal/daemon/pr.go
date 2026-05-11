package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/shinpr/galley/internal/inputfiles"
	"github.com/shinpr/galley/internal/strutil"
	"github.com/shinpr/galley/internal/task"
	"github.com/shinpr/galley/internal/vcs"
	"github.com/shinpr/galley/internal/workspace"
)

func finalizeAcceptedChange(ctx context.Context, opts Options, loaded *task.Task, workDir, baseSHA, runDir string) error {
	prBodyPath := filepath.Join(runDir, "pr_body.md")
	if err := os.WriteFile(prBodyPath, []byte(renderPRBody(*loaded)), 0o600); err != nil {
		return fmt.Errorf("write pr body: %w", err)
	}
	if err := inputfiles.CleanupNonCommitted(workDir, loaded.Files); err != nil {
		return err
	}

	snapshot, snapshotErr := workspace.CaptureSnapshotFromBase(ctx, workDir, baseSHA, workspaceOptions(opts))
	if snapshotErr == nil && !snapshot.Dirty {
		if loaded.PR.URL != "" {
			loaded.PR.Status = "open"
			return nil
		}
		if !opts.OpenPR {
			loaded.PR.Status = "not_requested"
			return nil
		}
		return fmt.Errorf("accepted task has no git diff to commit and no existing PR")
	}
	if snapshotErr != nil {
		return fmt.Errorf("capture final diff: %w", snapshotErr)
	}
	if err := ensureNonCommittedInputsAbsentFromBranch(snapshot, loaded.Files); err != nil {
		return err
	}

	if snapshot.StatusPorcelain != "" {
		commitMessage := fmt.Sprintf("galley: %s", strutil.FirstNonEmpty(loaded.ID, "accepted task"))
		if err := vcs.AddAllowedPaths(ctx, vcsBinaries(opts), workDir, runDir, loaded.Scope.AllowedPaths, loaded.Scope.ForbiddenPaths); err != nil {
			return err
		}
		if err := vcs.Commit(ctx, vcsBinaries(opts), workDir, runDir, commitMessage); err != nil {
			return err
		}
	}
	if !opts.OpenPR {
		loaded.PR.Status = "not_requested"
		return nil
	}
	if err := vcs.PushCurrentBranch(ctx, vcsBinaries(opts), workDir, runDir); err != nil {
		return err
	}
	if loaded.PR.URL != "" {
		loaded.PR.Status = "open"
		return nil
	}
	prURL, err := vcs.CreatePullRequest(ctx, vcsBinaries(opts), workDir, runDir, prBodyPath, opts.PRBase, prTitle(*loaded))
	if err != nil {
		return err
	}
	loaded.PR.URL = prURL
	loaded.PR.Status = "open"
	// Persist the PR author so later PR comment authorization can verify the
	// commenter is the same user without re-fetching from GitHub. An author
	// lookup failure is recorded as a risk rather than blocking acceptance:
	// the PR has been created, and comment polling fails closed when the
	// stored author is empty.
	authorLogin, authorErr := vcs.FetchPRAuthorLogin(ctx, vcsBinaries(opts), workDir, prURL)
	if authorErr != nil {
		loaded.Risks = append(loaded.Risks, task.Risk{
			ID:                   "pr-author-lookup-" + strutil.FirstNonEmpty(loaded.ID, "task"),
			Type:                 "external_dependency",
			Detail:               fmt.Sprintf("Galley created the PR but could not record its author login: %v", authorErr),
			Mitigation:           "Re-run `gh api repos/{owner}/{repo}/pulls/{number}` after the GitHub API is reachable and set pr.author_login on the task YAML, or expect Galley to reject /galley PR comments until the author is known.",
			HumanReviewSuggested: true,
		})
		return nil
	}
	loaded.PR.AuthorLogin = authorLogin
	return nil
}

func ensureNonCommittedInputsAbsentFromBranch(snapshot workspace.Snapshot, files []task.InputFile) error {
	branchFiles := make(map[string]bool, len(snapshot.BranchFiles))
	for _, file := range snapshot.BranchFiles {
		branchFiles[filepath.ToSlash(filepath.Clean(file))] = true
	}
	for _, file := range files {
		if file.Commit || file.Destination == "" {
			continue
		}
		dst := filepath.ToSlash(filepath.Clean(file.Destination))
		if branchFiles[dst] {
			return fmt.Errorf("non-committed input file %s is present in committed branch diff", file.Destination)
		}
	}
	return nil
}

// prTitleRuneBudget is the soft visual cap on the number of runes Galley keeps
// in a generated PR title. Most real goals are ASCII or close to it, so this
// keeps the title visually short. The hard limit that protects against GitHub
// rejecting the request is prTitleByteBudget below.
const prTitleRuneBudget = 72

// prTitleByteBudget is the hard byte cap GitHub enforces on PR titles. A
// 72-rune title can still exceed 256 bytes when every rune is a 4-byte UTF-8
// character (for example an emoji such as 🎉, which is 4 bytes, so 72*4=288).
// Any output of prTitle must satisfy len(title) <= prTitleByteBudget.
const prTitleByteBudget = 256

// prTitleEllipsis marks that prTitle truncated the original task goal so the
// reader can see the title is shortened. "…" (U+2026) is 3 bytes in UTF-8.
const prTitleEllipsis = "…"

func prTitle(loaded task.Task) string {
	title := strings.TrimSpace(loaded.Goal)
	if title == "" {
		title = loaded.ID
	}
	title = strings.ReplaceAll(title, "\n", " ")
	// First pass: enforce the visual rune cap with whitespace-preferred
	// truncation. Most goals fit and are returned untouched.
	title = truncatePRTitleByRunes(title)
	// Second pass: enforce GitHub's hard byte cap. The rune cap can still
	// exceed 256 bytes when every rune is a 4-byte UTF-8 character, so this
	// pass guarantees the result fits while preserving valid UTF-8 and the
	// ellipsis marker.
	title = truncatePRTitleByBytes(title)
	return title
}

// truncatePRTitleByRunes enforces prTitleRuneBudget while preferring to cut at
// a whitespace boundary so the trailing context is not lost mid-word.
func truncatePRTitleByRunes(title string) string {
	runes := []rune(title)
	if len(runes) <= prTitleRuneBudget {
		return title
	}
	// Reserve one rune for the ellipsis marker and try to break at a safe word
	// boundary inside the remaining budget so the trailing context is not cut
	// mid-word.
	keep := prTitleRuneBudget - len([]rune(prTitleEllipsis))
	if keep < 1 {
		keep = 1
	}
	cut := keep
	for i := keep - 1; i >= 0; i-- {
		if isPRTitleBreakRune(runes[i]) {
			cut = i
			break
		}
	}
	// Drop trailing whitespace before the ellipsis so the marker reads cleanly.
	trimmed := strings.TrimRightFunc(string(runes[:cut]), func(r rune) bool {
		return isPRTitleBreakRune(r)
	})
	if trimmed == "" {
		// No usable break boundary inside the budget; fall back to a hard cut
		// so callers still receive a meaningful title prefix.
		trimmed = string(runes[:keep])
	}
	return trimmed + prTitleEllipsis
}

// truncatePRTitleByBytes enforces prTitleByteBudget. The cut always lands on a
// valid UTF-8 rune boundary; the function prefers a whitespace boundary inside
// the budget and falls back to the nearest rune boundary otherwise. The
// ellipsis marker is always appended when truncation actually trims the input.
func truncatePRTitleByBytes(title string) string {
	if len(title) <= prTitleByteBudget {
		return title
	}
	ellipsisBytes := len(prTitleEllipsis)
	keep := prTitleByteBudget - ellipsisBytes
	if keep < 0 {
		keep = 0
	}
	// Walk runes, tracking the largest byte-prefix that fits in keep bytes
	// while staying on a valid UTF-8 boundary.
	end := 0
	lastBreak := -1
	for i, r := range title {
		runeEnd := i + utf8.RuneLen(r)
		if runeEnd > keep {
			break
		}
		end = runeEnd
		if isPRTitleBreakRune(r) {
			// Record the byte index BEFORE the whitespace rune so callers
			// can drop the whitespace itself when cutting.
			lastBreak = i
		}
	}
	cut := end
	if lastBreak >= 0 {
		cut = lastBreak
	}
	candidate := strings.TrimRightFunc(title[:cut], isPRTitleBreakRune)
	if candidate == "" {
		// No usable whitespace boundary inside the byte budget; fall back to
		// the largest rune-aligned prefix so callers still receive a
		// meaningful title prefix.
		candidate = title[:end]
	}
	return candidate + prTitleEllipsis
}

func isPRTitleBreakRune(r rune) bool {
	switch r {
	case ' ', '\t', '\n', '\r', '　':
		return true
	}
	return false
}

func renderPRBody(loaded task.Task) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Goal\n\n%s\n\n", loaded.Goal)
	b.WriteString("## Acceptance Criteria\n\n")
	for _, ac := range loaded.AcceptanceCriteria {
		fmt.Fprintf(&b, "- `%s` %s\n  - Verification: %s\n  - Status: %s\n", ac.ID, ac.Text, ac.Verification, ac.Status)
	}
	if commands := finalVerificationCommands(loaded.Verification.Commands); len(commands) > 0 {
		b.WriteString("\n## Final Verification\n\n")
		for _, command := range commands {
			fmt.Fprintf(&b, "- `%s`: %s\n", command.Cmd, command.Status)
		}
	}
	if decisions := prVisibleDecisions(loaded.Decisions); len(decisions) > 0 {
		b.WriteString("\n## Key Decisions\n\n")
		for _, decision := range decisions {
			fmt.Fprintf(&b, "- `%s` %s -> %s\n", decision.ID, decision.Question, decision.Chosen)
			if decision.Rationale != "" {
				fmt.Fprintf(&b, "  - Rationale: %s\n", decision.Rationale)
			}
			if decision.Reversibility != "" {
				fmt.Fprintf(&b, "  - Reversibility: %s\n", decision.Reversibility)
			}
			if decision.NeedsHumanReview {
				fmt.Fprintf(&b, "  - Human review suggested: true\n")
			}
		}
	}
	if len(loaded.DiscussionItems) > 0 {
		b.WriteString("\n## Discussion Items\n\n")
		for _, item := range loaded.DiscussionItems {
			fmt.Fprintf(&b, "- `%s` %s: %s\n", item.ID, item.Topic, item.Summary)
			if item.RequiresHumanDecision {
				fmt.Fprintf(&b, "  - Human decision required: true\n")
			}
		}
	}
	risks := prVisibleRisks(loaded)
	if len(risks) > 0 {
		b.WriteString("\n## Risks\n\n")
		for _, risk := range risks {
			fmt.Fprintf(&b, "- `%s` %s: %s\n  - Mitigation: %s\n", risk.ID, risk.Type, risk.Detail, risk.Mitigation)
		}
	}
	return b.String()
}

func finalVerificationCommands(commands []task.VerificationCommand) []task.VerificationCommand {
	seen := make(map[string]bool, len(commands))
	var reversed []task.VerificationCommand
	for i := len(commands) - 1; i >= 0; i-- {
		cmd := strings.TrimSpace(commands[i].Cmd)
		if cmd == "" || seen[cmd] || commands[i].Status != "passed" || cmd == "claude -p" {
			continue
		}
		seen[cmd] = true
		reversed = append(reversed, commands[i])
	}
	final := make([]task.VerificationCommand, 0, len(reversed))
	for i := len(reversed) - 1; i >= 0; i-- {
		final = append(final, reversed[i])
	}
	return final
}

func prVisibleDecisions(decisions []task.Decision) []task.Decision {
	seen := make(map[string]bool, len(decisions))
	var reversed []task.Decision
	for i := len(decisions) - 1; i >= 0; i-- {
		key := strings.TrimSpace(decisions[i].Question)
		if key == "" {
			key = strings.TrimSpace(decisions[i].Chosen)
		}
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		reversed = append(reversed, decisions[i])
	}
	final := make([]task.Decision, 0, len(reversed))
	for i := len(reversed) - 1; i >= 0; i-- {
		final = append(final, reversed[i])
	}
	return final
}

func prVisibleRisks(loaded task.Task) []task.Risk {
	var risks []task.Risk
	for _, risk := range loaded.Risks {
		if isResolvedAttemptRisk(risk) {
			continue
		}
		risks = append(risks, risk)
	}
	return risks
}

func isResolvedAttemptRisk(risk task.Risk) bool {
	for _, prefix := range []string{
		"workspace-dirty-",
		"claude-risk-",
		"git-diff-empty-",
		"claude-result-parse-",
		"git-diff-",
	} {
		if strings.HasPrefix(risk.ID, prefix) {
			return true
		}
	}
	return false
}
