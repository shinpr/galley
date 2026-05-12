package vcs

import (
	"context"
	"encoding/json"
	"errors"
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
	ID      int64         `json:"id"`
	Body    string        `json:"body"`
	HTMLURL string        `json:"html_url"`
	User    PRCommentUser `json:"user"`
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

// Binaries identifies the git and gh executables used by VCS operations.
type Binaries struct {
	Git string
	GH  string
}

func (b Binaries) git() string {
	if b.Git != "" {
		return b.Git
	}
	return "git"
}

func (b Binaries) gh() string {
	if b.GH != "" {
		return b.GH
	}
	return "gh"
}

// AddPaths stages the provided worktree-relative paths.
func AddPaths(ctx context.Context, bins Binaries, workDir, runDir string, paths []string) error {
	stagePaths := make([]string, 0, len(paths))
	seen := make(map[string]bool, len(paths))
	for _, path := range paths {
		clean := filepath.ToSlash(filepath.Clean(path))
		if clean == "" || clean == "." || seen[clean] {
			continue
		}
		seen[clean] = true
		stagePaths = append(stagePaths, clean)
	}
	if len(stagePaths) == 0 {
		return fmt.Errorf("git add paths is empty")
	}
	argv := append([]string{bins.git(), "add", "-A", "--"}, stagePaths...)
	result, err := runner.RunCommand(ctx, runner.Command{
		WorkDir: workDir,
		Argv:    argv,
	}, runner.RunOptions{
		StdoutPath: filepath.Join(runDir, "git_add.stdout.log"),
		StderrPath: filepath.Join(runDir, "git_add.stderr.log"),
	})
	writeErr := writeJSON(filepath.Join(runDir, "git_add_result.json"), result)
	if err != nil {
		return errors.Join(fmt.Errorf("git add failed: %w", err), writeErr)
	}
	if writeErr != nil {
		return writeErr
	}
	return nil
}

// Commit creates a git commit and writes command evidence.
func Commit(ctx context.Context, bins Binaries, workDir, runDir, message string) error {
	result, err := runner.RunCommand(ctx, runner.Command{
		WorkDir: workDir,
		Argv:    []string{bins.git(), "commit", "-m", message},
	}, runner.RunOptions{
		StdoutPath: filepath.Join(runDir, "git_commit.stdout.log"),
		StderrPath: filepath.Join(runDir, "git_commit.stderr.log"),
	})
	writeErr := writeJSON(filepath.Join(runDir, "git_commit_result.json"), result)
	if err != nil {
		return errors.Join(fmt.Errorf("git commit failed: %w", err), writeErr)
	}
	if writeErr != nil {
		return writeErr
	}
	return nil
}

// PushCurrentBranch pushes HEAD to origin and writes command evidence.
func PushCurrentBranch(ctx context.Context, bins Binaries, workDir, runDir string) error {
	result, err := runner.RunCommand(ctx, runner.Command{
		WorkDir: workDir,
		Argv:    []string{bins.git(), "push", "-u", "origin", "HEAD"},
	}, runner.RunOptions{
		StdoutPath: filepath.Join(runDir, "git_push.stdout.log"),
		StderrPath: filepath.Join(runDir, "git_push.stderr.log"),
	})
	writeErr := writeJSON(filepath.Join(runDir, "git_push_result.json"), result)
	if err != nil {
		return errors.Join(fmt.Errorf("git push failed: %w", err), writeErr)
	}
	if writeErr != nil {
		return writeErr
	}
	return nil
}

// CreatePullRequest opens a GitHub PR with gh and writes command evidence.
func CreatePullRequest(ctx context.Context, bins Binaries, workDir, runDir, bodyPath, base, title string) (string, error) {
	absoluteBodyPath, err := filepath.Abs(bodyPath)
	if err != nil {
		return "", fmt.Errorf("resolve pr body path: %w", err)
	}
	argv := []string{bins.gh(), "pr", "create", "--title", title, "--body-file", absoluteBodyPath}
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
	writeErr := writeJSON(filepath.Join(runDir, "gh_pr_create_result.json"), result)
	if err != nil {
		return "", errors.Join(fmt.Errorf("gh pr create failed: %w", err), writeErr)
	}
	if writeErr != nil {
		return "", writeErr
	}
	prURL := ExtractFirstHTTPSURL(result.Stdout)
	if prURL == "" {
		return "", fmt.Errorf("gh pr create returned an empty URL")
	}
	return prURL, nil
}

// FetchPRComments returns all PR comments using gh api pagination.
func FetchPRComments(ctx context.Context, bins Binaries, root, prURL string) ([]PRComment, error) {
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
		Argv:    []string{bins.gh(), "api", apiPath, "--paginate", "--slurp"},
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
func PostPRComment(ctx context.Context, bins Binaries, root, prURL, body string) error {
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
		Argv:    []string{bins.gh(), "api", "-X", "POST", apiPath, "--input", "-"},
		Stdin:   string(payload),
	}, runner.RunOptions{})
	if err != nil {
		return fmt.Errorf("gh api post PR comment failed: %w", err)
	}
	return nil
}

// FetchPRAuthorLogin returns the GitHub login of the user who opened the PR.
// Galley persists the result on the task PR record at PR creation time so
// later PR comment authorization can verify the comment author matches the
// PR author without re-fetching from GitHub.
func FetchPRAuthorLogin(ctx context.Context, bins Binaries, root, prURL string) (string, error) {
	owner, repo, number, err := ParseGitHubPRURL(prURL)
	if err != nil {
		return "", err
	}
	apiPath := fmt.Sprintf("repos/%s/%s/pulls/%s", owner, repo, number)
	stdoutFile, err := os.CreateTemp("", "galley-pr-author-*.json")
	if err != nil {
		return "", fmt.Errorf("create PR author temp file: %w", err)
	}
	stdoutPath := stdoutFile.Name()
	if err := stdoutFile.Close(); err != nil {
		return "", fmt.Errorf("close PR author temp file: %w", err)
	}
	defer os.Remove(stdoutPath)
	_, err = runner.RunCommand(ctx, runner.Command{
		WorkDir: root,
		Argv:    []string{bins.gh(), "api", apiPath},
	}, runner.RunOptions{StdoutPath: stdoutPath})
	if err != nil {
		return "", fmt.Errorf("gh api PR author failed: %w", err)
	}
	output, err := os.ReadFile(stdoutPath)
	if err != nil {
		return "", fmt.Errorf("read PR author response: %w", err)
	}
	var payload struct {
		User struct {
			Login string `json:"login"`
		} `json:"user"`
	}
	if err := json.Unmarshal(output, &payload); err != nil {
		return "", fmt.Errorf("decode PR author: %w", err)
	}
	if payload.User.Login == "" {
		// Treat a missing/empty user.login as a lookup failure so callers can
		// surface a pr-author-lookup risk and downstream PR comment trust
		// checks fail closed instead of silently accepting an empty author.
		return "", fmt.Errorf("PR author lookup returned empty user.login")
	}
	return payload.User.Login, nil
}

// FetchPRState returns the current GitHub PR state via gh api.
func FetchPRState(ctx context.Context, bins Binaries, root, prURL string) (PullRequestState, error) {
	owner, repo, number, err := ParseGitHubPRURL(prURL)
	if err != nil {
		return PullRequestState{}, err
	}
	apiPath := fmt.Sprintf("repos/%s/%s/pulls/%s", owner, repo, number)
	stdoutFile, err := os.CreateTemp("", "galley-pr-state-*.json")
	if err != nil {
		return PullRequestState{}, fmt.Errorf("create PR state temp file: %w", err)
	}
	stdoutPath := stdoutFile.Name()
	if err := stdoutFile.Close(); err != nil {
		return PullRequestState{}, fmt.Errorf("close PR state temp file: %w", err)
	}
	defer os.Remove(stdoutPath)
	_, err = runner.RunCommand(ctx, runner.Command{
		WorkDir: root,
		Argv:    []string{bins.gh(), "api", apiPath},
	}, runner.RunOptions{StdoutPath: stdoutPath})
	if err != nil {
		return PullRequestState{}, fmt.Errorf("gh api PR state failed: %w", err)
	}
	output, err := os.ReadFile(stdoutPath)
	if err != nil {
		return PullRequestState{}, fmt.Errorf("read PR state response: %w", err)
	}
	var state PullRequestState
	if err := json.Unmarshal(output, &state); err != nil {
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
	return strings.TrimRight(httpsURLPattern.FindString(strings.TrimSpace(stdout)), ".,);]")
}

func writeJSON(path string, value any) error {
	return jsonio.Write(path, value)
}
