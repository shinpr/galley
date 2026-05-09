package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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

func prTitle(loaded task.Task) string {
	title := strings.TrimSpace(loaded.Goal)
	if title == "" {
		title = loaded.ID
	}
	title = strings.ReplaceAll(title, "\n", " ")
	runes := []rune(title)
	if len(runes) > 72 {
		title = string(runes[:72])
	}
	return title
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
