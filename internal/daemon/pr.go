package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shinpr/galley/internal/runner"
	"github.com/shinpr/galley/internal/task"
)

func finalizeAcceptedChange(ctx context.Context, opts Options, loaded *task.Task, workDir, runDir, runID string) error {
	prBodyPath := filepath.Join(runDir, "pr_body.md")
	if err := os.WriteFile(prBodyPath, []byte(renderPRBody(*loaded, runID)), 0o644); err != nil {
		return fmt.Errorf("write pr body: %w", err)
	}

	commitMessage := fmt.Sprintf("galley: %s", firstNonEmpty(loaded.ID, "accepted task"))
	if err := gitAddAll(ctx, workDir, runDir); err != nil {
		return err
	}
	if err := gitCommit(ctx, workDir, runDir, commitMessage); err != nil {
		return err
	}
	if !opts.OpenPR {
		loaded.PR.Status = "not_requested"
		return nil
	}
	if err := gitPush(ctx, workDir, runDir); err != nil {
		return err
	}
	prURL, err := createPullRequest(ctx, workDir, runDir, prBodyPath, opts.PRBase, prTitle(*loaded))
	if err != nil {
		return err
	}
	loaded.PR.URL = prURL
	loaded.PR.Status = "open"
	return nil
}

func gitAddAll(ctx context.Context, workDir, runDir string) error {
	result, err := runner.RunCommand(ctx, runner.ClaudeCommand{
		WorkDir: workDir,
		Argv:    []string{"git", "add", "-A"},
	}, runner.RunOptions{
		StdoutPath: filepath.Join(runDir, "git_add.stdout.log"),
		StderrPath: filepath.Join(runDir, "git_add.stderr.log"),
	})
	if err != nil {
		return fmt.Errorf("git add failed: %w", err)
	}
	return writeJSON(filepath.Join(runDir, "git_add_result.json"), result)
}

func gitCommit(ctx context.Context, workDir, runDir, message string) error {
	result, err := runner.RunCommand(ctx, runner.ClaudeCommand{
		WorkDir: workDir,
		Argv:    []string{"git", "commit", "-m", message},
	}, runner.RunOptions{
		StdoutPath: filepath.Join(runDir, "git_commit.stdout.log"),
		StderrPath: filepath.Join(runDir, "git_commit.stderr.log"),
	})
	if writeErr := writeJSON(filepath.Join(runDir, "git_commit_result.json"), result); writeErr != nil {
		return writeErr
	}
	if err != nil {
		return fmt.Errorf("git commit failed: %w", err)
	}
	return nil
}

func gitPush(ctx context.Context, workDir, runDir string) error {
	result, err := runner.RunCommand(ctx, runner.ClaudeCommand{
		WorkDir: workDir,
		Argv:    []string{"git", "push", "-u", "origin", "HEAD"},
	}, runner.RunOptions{
		StdoutPath: filepath.Join(runDir, "git_push.stdout.log"),
		StderrPath: filepath.Join(runDir, "git_push.stderr.log"),
	})
	if writeErr := writeJSON(filepath.Join(runDir, "git_push_result.json"), result); writeErr != nil {
		return writeErr
	}
	if err != nil {
		return fmt.Errorf("git push failed: %w", err)
	}
	return nil
}

func createPullRequest(ctx context.Context, workDir, runDir, bodyPath, base, title string) (string, error) {
	argv := []string{"gh", "pr", "create", "--title", title, "--body-file", bodyPath}
	if base != "" {
		argv = append(argv, "--base", base)
	}
	result, err := runner.RunCommand(ctx, runner.ClaudeCommand{
		WorkDir: workDir,
		Argv:    argv,
	}, runner.RunOptions{
		StdoutPath: filepath.Join(runDir, "gh_pr_create.stdout.log"),
		StderrPath: filepath.Join(runDir, "gh_pr_create.stderr.log"),
	})
	if writeErr := writeJSON(filepath.Join(runDir, "gh_pr_create_result.json"), result); writeErr != nil {
		return "", writeErr
	}
	if err != nil {
		return "", fmt.Errorf("gh pr create failed: %w", err)
	}
	url := strings.TrimSpace(result.Stdout)
	if url == "" {
		return "", fmt.Errorf("gh pr create returned an empty URL")
	}
	if fields := strings.Fields(url); len(fields) > 0 {
		url = fields[len(fields)-1]
	}
	return url, nil
}

func prTitle(loaded task.Task) string {
	title := strings.TrimSpace(loaded.Goal)
	if title == "" {
		title = loaded.ID
	}
	title = strings.ReplaceAll(title, "\n", " ")
	if len(title) > 72 {
		title = title[:72]
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
		}
	}
	if len(loaded.Risks) > 0 {
		b.WriteString("\n## Risks\n\n")
		for _, risk := range loaded.Risks {
			fmt.Fprintf(&b, "- `%s` %s: %s\n  - Mitigation: %s\n", risk.ID, risk.Type, risk.Detail, risk.Mitigation)
		}
	}
	return b.String()
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
