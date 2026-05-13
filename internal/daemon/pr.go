package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/shinpr/galley/internal/inputfiles"
	"github.com/shinpr/galley/internal/retry"
	"github.com/shinpr/galley/internal/strutil"
	"github.com/shinpr/galley/internal/task"
	"github.com/shinpr/galley/internal/vcs"
	"github.com/shinpr/galley/internal/workspace"
)

func finalizeAcceptedChange(ctx context.Context, opts Options, loaded *task.Task, workDir, baseSHA, runDir string) error {
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
	changedFiles := sortedChangedFiles(snapshot)
	if forbidden := pathsInsideScope(changedFiles, loaded.Scope.ForbiddenPaths); len(forbidden) > 0 {
		return fmt.Errorf("accepted diff changes paths inside task.scope.forbidden_paths: %s", strings.Join(forbidden, ", "))
	}
	addScopeExpansionDiscussion(loaded, changedFiles)

	prBodyPath := filepath.Join(runDir, "pr_body.md")
	if err := os.WriteFile(prBodyPath, []byte(renderPRBody(*loaded)), 0o600); err != nil {
		return fmt.Errorf("write pr body: %w", err)
	}
	if snapshot.StatusPorcelain != "" {
		commitMessage := fmt.Sprintf("galley: %s", strutil.FirstNonEmpty(loaded.ID, "accepted task"))
		if err := vcs.AddPaths(ctx, vcsBinaries(opts), workDir, runDir, parsePorcelainPaths(snapshot.StatusPorcelain)); err != nil {
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
	// Retry `git push -u origin HEAD`. Pushing the same ref is idempotent in
	// practice: re-running the push after a transport flake either no-ops if
	// the remote already has the SHA or surfaces a non-fast-forward error
	// unchanged for the caller to handle.
	if err := retry.Do(ctx, func(ctx context.Context) error {
		return vcs.PushCurrentBranch(ctx, vcsBinaries(opts), workDir, runDir)
	}); err != nil {
		return err
	}
	if loaded.PR.URL != "" {
		loaded.PR.Status = "open"
		return nil
	}
	// Retry `gh pr create`. gh CLI fails fast with a clear error when a PR
	// already exists for the branch, so a duplicate-create retry exhausts the
	// bounded backoff and surfaces the gh error unchanged.
	var prURL string
	err := retry.Do(ctx, func(ctx context.Context) error {
		url, createErr := vcs.CreatePullRequest(ctx, vcsBinaries(opts), workDir, runDir, prBodyPath, opts.PRBase, prTitle(*loaded))
		if createErr != nil {
			return createErr
		}
		prURL = url
		return nil
	})
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
	// Retry `gh api repos/{owner}/{repo}/pulls/{number}` so a transient GitHub
	// read flake right after PR creation does not lose the recorded author
	// login. GET is idempotent, so retries are safe.
	var authorLogin string
	authorErr := retry.Do(ctx, func(ctx context.Context) error {
		login, fetchErr := vcs.FetchPRAuthorLogin(ctx, vcsBinaries(opts), workDir, prURL)
		if fetchErr != nil {
			return fetchErr
		}
		authorLogin = login
		return nil
	})
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

func sortedChangedFiles(snapshot workspace.Snapshot) []string {
	changed := changedFilesFromSnapshot(snapshot)
	files := make([]string, 0, len(changed))
	for file := range changed {
		clean := filepath.ToSlash(filepath.Clean(file))
		if clean != "" && clean != "." {
			files = append(files, clean)
		}
	}
	sort.Strings(files)
	return files
}

func pathsInsideScope(paths, prefixes []string) []string {
	var matches []string
	for _, path := range paths {
		if pathInsideAnyPrefix(path, prefixes) {
			matches = append(matches, path)
		}
	}
	return matches
}

func pathsOutsideScope(paths, prefixes []string) []string {
	var matches []string
	for _, path := range paths {
		if !pathInsideAnyPrefix(path, prefixes) {
			matches = append(matches, path)
		}
	}
	return matches
}

func pathInsideAnyPrefix(path string, prefixes []string) bool {
	cleanPath := filepath.ToSlash(filepath.Clean(path))
	for _, prefix := range prefixes {
		cleanPrefix := filepath.ToSlash(filepath.Clean(prefix))
		if cleanPrefix == "." || cleanPrefix == cleanPath || strings.HasPrefix(cleanPath, cleanPrefix+"/") {
			return true
		}
	}
	return false
}

func addScopeExpansionDiscussion(loaded *task.Task, changedFiles []string) {
	outsideAllowed := pathsOutsideScope(changedFiles, loaded.Scope.AllowedPaths)
	if len(outsideAllowed) == 0 {
		return
	}
	loaded.DiscussionItems = append(loaded.DiscussionItems, task.DiscussionItem{
		ID:                    fmt.Sprintf("discussion-%d", len(loaded.DiscussionItems)+1),
		Topic:                 "Scope expansion",
		Summary:               fmt.Sprintf("Accepted diff includes changes outside task.scope.allowed_paths: %s. Review whether this scope expansion belongs in this PR.", strings.Join(outsideAllowed, ", ")),
		RequiresHumanDecision: true,
	})
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
// budget sits close to (but below) GitHub's 256-byte hard limit so ordinary
// long ASCII task goals are preserved without a premature ellipsis. The hard
// limit that protects against GitHub rejecting the request is
// prTitleByteBudget below; titles whose UTF-8 encoding still exceeds that
// byte cap (for example emoji-heavy goals) are trimmed by the byte-budget
// pass.
const prTitleRuneBudget = 240

// prTitleByteBudget is the hard byte cap GitHub enforces on PR titles. A
// rune-budgeted title can still exceed 256 bytes when every rune is a 4-byte
// UTF-8 character (for example an emoji such as 🎉, which is 4 bytes, so
// 240*4=960). Any output of prTitle must satisfy len(title) <= prTitleByteBudget.
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
