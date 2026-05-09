package vcs

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/shinpr/galley/internal/jsonio"
	"github.com/shinpr/galley/internal/runner"
)

// PRComment is the subset of a GitHub issue comment Galley consumes.
type PRComment struct {
	ID                int64         `json:"id"`
	Body              string        `json:"body"`
	HTMLURL           string        `json:"html_url"`
	AuthorAssociation string        `json:"author_association"`
	User              PRCommentUser `json:"user"`
}

// PRCommentUser is the subset of a GitHub comment user Galley records.
type PRCommentUser struct {
	Login string `json:"login"`
}

// PullRequestState is the subset of GitHub PR state Galley consumes.
type PullRequestState struct {
	State  string `json:"state"`
	Merged bool   `json:"merged"`
}

// AddAllowedPaths stages only the task allowed paths and unstages forbidden paths.
func AddAllowedPaths(ctx context.Context, workDir, runDir string, allowedPaths, forbiddenPaths []string) error {
	if len(allowedPaths) == 0 {
		return fmt.Errorf("git add allowed paths is empty")
	}
	argv := append([]string{"git", "add", "-A", "--"}, allowedPaths...)
	result, err := runner.RunCommand(ctx, runner.Command{
		WorkDir: workDir,
		Argv:    argv,
	}, runner.RunOptions{
		StdoutPath: filepath.Join(runDir, "git_add.stdout.log"),
		StderrPath: filepath.Join(runDir, "git_add.stderr.log"),
	})
	if err != nil {
		return fmt.Errorf("git add failed: %w", err)
	}
	if err := writeJSON(filepath.Join(runDir, "git_add_result.json"), result); err != nil {
		return err
	}
	if len(forbiddenPaths) == 0 {
		return nil
	}
	resetArgv := append([]string{"git", "reset", "-q", "--"}, forbiddenPaths...)
	resetResult, err := runner.RunCommand(ctx, runner.Command{
		WorkDir: workDir,
		Argv:    resetArgv,
	}, runner.RunOptions{
		StdoutPath: filepath.Join(runDir, "git_reset_forbidden.stdout.log"),
		StderrPath: filepath.Join(runDir, "git_reset_forbidden.stderr.log"),
	})
	if writeErr := writeJSON(filepath.Join(runDir, "git_reset_forbidden_result.json"), resetResult); writeErr != nil {
		return writeErr
	}
	if err != nil {
		return fmt.Errorf("git reset forbidden paths failed: %w", err)
	}
	return nil
}

// Commit creates a git commit and writes command evidence.
func Commit(ctx context.Context, workDir, runDir, message string) error {
	result, err := runner.RunCommand(ctx, runner.Command{
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

// PushCurrentBranch pushes HEAD to origin and writes command evidence.
func PushCurrentBranch(ctx context.Context, workDir, runDir string) error {
	result, err := runner.RunCommand(ctx, runner.Command{
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

// CreatePullRequest opens a GitHub PR with gh and writes command evidence.
func CreatePullRequest(ctx context.Context, workDir, runDir, bodyPath, base, title string) (string, error) {
	absoluteBodyPath, err := filepath.Abs(bodyPath)
	if err != nil {
		return "", fmt.Errorf("resolve pr body path: %w", err)
	}
	argv := []string{"gh", "pr", "create", "--title", title, "--body-file", absoluteBodyPath}
	if base != "" {
		argv = append(argv, "--base", base)
	}
	result, err := runner.RunCommand(ctx, runner.Command{
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
	prURL := ExtractFirstHTTPSURL(result.Stdout)
	if prURL == "" {
		return "", fmt.Errorf("gh pr create returned an empty URL")
	}
	return prURL, nil
}

// FetchPRComments returns all PR comments using gh api pagination.
func FetchPRComments(ctx context.Context, root, prURL string) ([]PRComment, error) {
	owner, repo, number, err := ParseGitHubPRURL(prURL)
	if err != nil {
		return nil, err
	}
	apiPath := fmt.Sprintf("repos/%s/%s/issues/%s/comments", owner, repo, number)
	stdoutFile, err := os.CreateTemp("", "galley-pr-comments-*.json")
	if err != nil {
		return nil, fmt.Errorf("create PR comments temp file: %w", err)
	}
	stdoutPath := stdoutFile.Name()
	if err := stdoutFile.Close(); err != nil {
		return nil, fmt.Errorf("close PR comments temp file: %w", err)
	}
	defer os.Remove(stdoutPath)
	_, err = runner.RunCommand(ctx, runner.Command{
		WorkDir: root,
		Argv:    []string{"gh", "api", apiPath, "--paginate", "--slurp"},
	}, runner.RunOptions{StdoutPath: stdoutPath})
	if err != nil {
		return nil, fmt.Errorf("gh api PR comments failed: %w", err)
	}
	output, err := os.ReadFile(stdoutPath)
	if err != nil {
		return nil, fmt.Errorf("read PR comments response: %w", err)
	}
	comments, err := DecodePRComments(string(output))
	if err != nil {
		return nil, fmt.Errorf("decode PR comments: %w", err)
	}
	return comments, nil
}

// PostPRComment posts a single GitHub PR comment via gh api.
func PostPRComment(ctx context.Context, root, prURL, body string) error {
	owner, repo, number, err := ParseGitHubPRURL(prURL)
	if err != nil {
		return err
	}
	apiPath := fmt.Sprintf("repos/%s/%s/issues/%s/comments", owner, repo, number)
	payload, err := json.Marshal(map[string]string{"body": body})
	if err != nil {
		return fmt.Errorf("encode PR comment body: %w", err)
	}
	_, err = runner.RunCommand(ctx, runner.Command{
		WorkDir: root,
		Argv:    []string{"gh", "api", "-X", "POST", apiPath, "--input", "-"},
		Stdin:   string(payload),
	}, runner.RunOptions{})
	if err != nil {
		return fmt.Errorf("gh api post PR comment failed: %w", err)
	}
	return nil
}

// FetchPRState returns the current GitHub PR state via gh api.
func FetchPRState(ctx context.Context, root, prURL string) (PullRequestState, error) {
	owner, repo, number, err := ParseGitHubPRURL(prURL)
	if err != nil {
		return PullRequestState{}, err
	}
	apiPath := fmt.Sprintf("repos/%s/%s/pulls/%s", owner, repo, number)
	result, err := runner.RunCommand(ctx, runner.Command{
		WorkDir: root,
		Argv:    []string{"gh", "api", apiPath},
	}, runner.RunOptions{})
	if err != nil {
		return PullRequestState{}, fmt.Errorf("gh api PR state failed: %w", err)
	}
	var state PullRequestState
	if err := json.Unmarshal([]byte(result.Stdout), &state); err != nil {
		return PullRequestState{}, fmt.Errorf("decode PR state: %w", err)
	}
	return state, nil
}

// DecodePRComments decodes gh api --slurp output, with a single-page fallback.
func DecodePRComments(stdout string) ([]PRComment, error) {
	var pages [][]PRComment
	if err := json.Unmarshal([]byte(stdout), &pages); err == nil {
		var comments []PRComment
		for _, page := range pages {
			comments = append(comments, page...)
		}
		return comments, nil
	}
	var comments []PRComment
	if err := json.Unmarshal([]byte(stdout), &comments); err != nil {
		return nil, err
	}
	return comments, nil
}

// ParseGitHubPRURL extracts owner, repository, and PR number from a GitHub PR URL.
func ParseGitHubPRURL(raw string) (string, string, string, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", "", "", fmt.Errorf("parse PR URL: %w", err)
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) < 4 || parts[2] != "pull" {
		return "", "", "", fmt.Errorf("unsupported PR URL: %s", raw)
	}
	if parts[0] == "" || parts[1] == "" || parts[3] == "" {
		return "", "", "", fmt.Errorf("unsupported PR URL: %s", raw)
	}
	if parsed.Scheme != "https" || !strings.EqualFold(parsed.Host, "github.com") {
		return "", "", "", fmt.Errorf("unsupported PR URL host: %s", raw)
	}
	if _, err := strconv.Atoi(parts[3]); err != nil {
		return "", "", "", fmt.Errorf("unsupported PR URL number: %s", raw)
	}
	return parts[0], parts[1], parts[3], nil
}

var httpsURLPattern = regexp.MustCompile(`https://[^\s]+`)

// ExtractFirstHTTPSURL returns the first URL-looking token from command output.
func ExtractFirstHTTPSURL(stdout string) string {
	return httpsURLPattern.FindString(strings.TrimSpace(stdout))
}

func writeJSON(path string, value any) error {
	return jsonio.Write(path, value)
}
