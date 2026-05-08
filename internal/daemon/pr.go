package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shinpr/galley/internal/task"
	"github.com/shinpr/galley/internal/vcs"
	"github.com/shinpr/galley/internal/workspace"
)

func finalizeAcceptedChange(ctx context.Context, opts Options, loaded *task.Task, workDir, runDir, runID string) error {
	prBodyPath := filepath.Join(runDir, "pr_body.md")
	if err := os.WriteFile(prBodyPath, []byte(renderPRBody(*loaded, runID)), 0o600); err != nil {
		return fmt.Errorf("write pr body: %w", err)
	}

	snapshot, snapshotErr := workspace.CaptureSnapshot(ctx, workDir)
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

	commitMessage := fmt.Sprintf("galley: %s", firstNonEmpty(loaded.ID, "accepted task"))
	if err := vcs.AddAllowedPaths(ctx, workDir, runDir, loaded.Scope.AllowedPaths); err != nil {
		return err
	}
	if err := vcs.Commit(ctx, workDir, runDir, commitMessage); err != nil {
		return err
	}
	if !opts.OpenPR {
		loaded.PR.Status = "not_requested"
		return nil
	}
	if err := vcs.PushCurrentBranch(ctx, workDir, runDir); err != nil {
		return err
	}
	if loaded.PR.URL != "" {
		loaded.PR.Status = "open"
		return nil
	}
	prURL, err := vcs.CreatePullRequest(ctx, workDir, runDir, prBodyPath, opts.PRBase, prTitle(*loaded))
	if err != nil {
		return err
	}
	loaded.PR.URL = prURL
	loaded.PR.Status = "open"
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

func renderPRBody(loaded task.Task, runID string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Goal\n\n%s\n\n", loaded.Goal)
	fmt.Fprintf(&b, "## Run Evidence\n\n- Task: `%s`\n- Run: `%s`\n- Generated: `%s`\n\n", loaded.ID, runID, time.Now().UTC().Format(time.RFC3339))
	b.WriteString("## Acceptance Criteria\n\n")
	for _, ac := range loaded.AcceptanceCriteria {
		fmt.Fprintf(&b, "- `%s` %s\n  - Verification: %s\n  - Status: %s\n", ac.ID, ac.Text, ac.Verification, ac.Status)
	}
	if len(loaded.Verification.Commands) > 0 {
		b.WriteString("\n## Verification\n\n")
		for _, command := range loaded.Verification.Commands {
			fmt.Fprintf(&b, "- `%s`: %s\n", command.Cmd, command.Status)
		}
	}
	if len(loaded.Decisions) > 0 {
		b.WriteString("\n## Decisions\n\n")
		for _, decision := range loaded.Decisions {
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
	risks := prVisibleRisks(loaded)
	if len(risks) > 0 {
		b.WriteString("\n## Risks\n\n")
		for _, risk := range risks {
			fmt.Fprintf(&b, "- `%s` %s: %s\n  - Mitigation: %s\n", risk.ID, risk.Type, risk.Detail, risk.Mitigation)
		}
	}
	return b.String()
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

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
