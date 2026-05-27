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
	"runtime"
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
//
// On Windows the pathspec list is delivered through stdin via
// --pathspec-from-file=- so a long list of changed files cannot push the
// CreateProcess command line past the Windows limit. macOS and Linux pass the
// pathspecs on argv, matching git's common local invocation shape.
func AddPaths(ctx context.Context, bins Binaries, workDir, runDir string, paths []string) error {
	return addPathsForOS(ctx, bins, workDir, runDir, paths, runtime.GOOS)
}

func addPathsForOS(ctx context.Context, bins Binaries, workDir, runDir string, paths []string, goos string) error {
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
	pathspecs := literalPathspecs(stagePaths)
	cmd := runner.Command{WorkDir: workDir}
	if goos == "windows" {
		// git add --pathspec-from-file=- --pathspec-file-nul reads pathspecs
		// from stdin so a long list of changed files never reaches argv. The
		// NUL separator avoids any ambiguity with paths that contain LF or
		// other whitespace characters.
		cmd.Argv = runner.GitArgs(bins.git(), "add", "-A", "--pathspec-from-file=-", "--pathspec-file-nul")
		cmd.Stdin = strings.Join(pathspecs, "\x00") + "\x00"
	} else {
		cmd.Argv = runner.GitArgs(bins.git(), append([]string{"add", "-A", "--"}, pathspecs...)...)
	}
	_, err := runCommandWithEvidence(ctx, cmd, runDir, "git_add", "git add failed")
	return err
}

// StatusPorcelainZ returns the NUL-separated `git status --porcelain=v1 -z`
// output for the worktree. Galley calls this in the review-staging step to
// discover the explicit set of executor-produced changes before computing
// the reviewable path set (the daemon-side representation of the submitted
// artifact). The function does not write evidence files because the caller
// captures the resolved reviewable path set itself in the staging command's
// argv evidence (see StagePathsForReview).
//
// --untracked-files=all is required: without it, git status collapses an
// untracked directory to a single trailing-slash entry (e.g. "docs/"), and a
// commit:false destination such as "docs/plan.md" would not match the
// directory-shaped entry. Galley would then stage the entire untracked
// directory and leak the context-only input into the supervisor diff. The
// `all` mode emits one entry per untracked file, which is the resolution
// the reviewable-path-set contract requires.
func StatusPorcelainZ(ctx context.Context, bins Binaries, workDir string) (string, error) {
	result, err := runner.RunCommand(ctx, runner.Command{
		WorkDir: workDir,
		Argv:    runner.GitArgs(bins.git(), "status", "--porcelain=v1", "-z", "--untracked-files=all"),
	}, runner.RunOptions{})
	if err != nil {
		return "", fmt.Errorf("git status --porcelain -z (review staging discovery) failed: %w", err)
	}
	return result.Stdout, nil
}

// StagePathsForReview stages the supplied worktree-relative paths so the
// supervisor diff snapshot captures the executor-produced change set Galley
// computed from `git status --porcelain` after the executor attempt. The
// caller is expected to have already:
//
// - discovered the change set with StatusPorcelainZ;
// - normalized and deduplicated each entry to slash form;
// - excluded task.files entries declared with commit:false so context-only
// inputs Galley materializes for the executor do not enter the
// submitted artifact.
//
// `paths` is therefore an explicit reviewable path set, not a wildcard or an
// `-A` over the whole worktree. The function runs
// `git add -A -- <path> [<path>...]` so paths that the executor added,
// modified, or deleted are all reflected in the index. On Windows the
// pathspecs are delivered via `--pathspec-from-file=- --pathspec-file-nul`
// so a long list of changed files cannot push the CreateProcess command
// line past the platform's argv limit; macOS and Linux continue to pass the
// pathspecs on argv.
//
// When `paths` is empty (the no-executor-change context-only case) the
// function records a "skipped" evidence payload at
// `<runDir>/git_add_review_result.json` and returns nil. Falling through to
// `git add -A` with no positional path would stage every dirty path in the
// worktree, defeating the explicit reviewable-set contract; falling through
// to `git add -A --` with no positional path would error on some git
// versions. Recording the skipped evidence preserves the invariant that
// the staging step is always observable in run evidence.
//
// On a non-skipped run the function writes:
//
// - git_add_review.stdout.log
// - git_add_review.stderr.log
// - git_add_review_result.json (runner.RunResult)
//
// A staging failure returns the original git error joined with any evidence
// write error so the caller can record a clear attempt failure instead of
// handing an empty or stale diff to the supervisor.
func StagePathsForReview(ctx context.Context, bins Binaries, workDir, runDir string, paths []string) error {
	return stagePathsForReviewForOS(ctx, bins, workDir, runDir, paths, runtime.GOOS)
}

func stagePathsForReviewForOS(ctx context.Context, bins Binaries, workDir, runDir string, paths []string, goos string) error {
	stagePaths := dedupeReviewPaths(paths)
	if len(stagePaths) == 0 {
		// The executor produced no reviewable change. Persist a skipped
		// evidence payload so reviewers can still confirm review staging
		// ran for this attempt and then return without calling
		// git add at all. Without this short-circuit, the alternative
		// `git add -A` over the whole worktree would re-stage every
		// dirty path (including context-only commit:false inputs),
		// defeating the explicit reviewable-set contract.
		return writeReviewStagingSkipped(runDir, "no executor-produced paths to stage")
	}
	pathspecs := literalPathspecs(stagePaths)
	cmd := runner.Command{WorkDir: workDir}
	if goos == "windows" {
		// On Windows the pathspec list is delivered through stdin so a
		// long list of changed files cannot push the CreateProcess command
		// line past the platform's argv limit. The NUL separator avoids
		// any ambiguity with paths that contain LF or other whitespace.
		cmd.Argv = runner.GitArgs(bins.git(), "add", "-A", "--pathspec-from-file=-", "--pathspec-file-nul")
		cmd.Stdin = strings.Join(pathspecs, "\x00") + "\x00"
	} else {
		cmd.Argv = runner.GitArgs(bins.git(), append([]string{"add", "-A", "--"}, pathspecs...)...)
	}
	result, err := runner.RunCommand(ctx, cmd, runner.RunOptions{
		StdoutPath: filepath.Join(runDir, "git_add_review.stdout.log"),
		StderrPath: filepath.Join(runDir, "git_add_review.stderr.log"),
	})
	writeErr := writeJSON(filepath.Join(runDir, "git_add_review_result.json"), result)
	if err != nil {
		return errors.Join(fmt.Errorf("git add -A (review staging) failed: %w", err), writeErr)
	}
	if writeErr != nil {
		return writeErr
	}
	return nil
}

// writeReviewStagingSkipped records a sentinel evidence payload when the
// review-staging step is intentionally skipped because there were no
// executor-produced paths to stage. The payload mirrors the field shape of
// the runner.RunResult-derived evidence used on the non-skipped path so
// readers can detect "skipped" by checking the dedicated key without
// pattern-matching command output.
func writeReviewStagingSkipped(runDir, reason string) error {
	return writeJSON(filepath.Join(runDir, "git_add_review_result.json"), map[string]any{
		"skipped": true,
		"reason":  reason,
	})
}

// dedupeReviewPaths normalizes and deduplicates worktree-relative review
// paths. It accepts already-normalized input from the daemon (see
// reviewablePathsFromStatus) and is defensive about callers that pass in
// pre-normalized but duplicated entries.
func dedupeReviewPaths(paths []string) []string {
	out := make([]string, 0, len(paths))
	seen := make(map[string]bool, len(paths))
	for _, p := range paths {
		clean := filepath.ToSlash(filepath.Clean(p))
		if clean == "" || clean == "." || seen[clean] {
			continue
		}
		seen[clean] = true
		out = append(out, clean)
	}
	return out
}

func literalPathspecs(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		out = append(out, ":(literal)"+path)
	}
	return out
}

// Commit creates a git commit and writes command evidence.
func Commit(ctx context.Context, bins Binaries, workDir, runDir, message string) error {
	_, err := runCommandWithEvidence(ctx, runner.Command{
		WorkDir: workDir,
		Argv:    runner.GitArgs(bins.git(), "commit", "-m", message),
	}, runDir, "git_commit", "git commit failed")
	return err
}

// PushCurrentBranch pushes HEAD to origin and writes command evidence.
func PushCurrentBranch(ctx context.Context, bins Binaries, workDir, runDir string) error {
	_, err := runCommandWithEvidence(ctx, runner.Command{
		WorkDir: workDir,
		Argv:    runner.GitArgs(bins.git(), "push", "-u", "origin", "HEAD"),
	}, runDir, "git_push", "git push failed")
	return err
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
	result, err := runCommandWithEvidence(ctx, runner.Command{
		WorkDir: workDir,
		Argv:    argv,
	}, runDir, "gh_pr_create", "gh pr create failed")
	if err != nil {
		return "", err
	}
	prURL := ExtractFirstHTTPSURL(result.Stdout)
	if prURL == "" {
		return "", fmt.Errorf("gh pr create returned an empty URL")
	}
	return prURL, nil
}

// FetchPRURLForCurrentBranch returns the URL of the open PR for the current
// branch of workDir, or an empty string when no PR exists for it. The call
// is read-only and safe to invoke as a recovery probe after a failed
// CreatePullRequest retry: if the create succeeded server-side but the
// response was lost, the URL is recovered; if no PR exists, the function
// returns "" with a nil error so the caller can surface the original
// create-failure unchanged.
func FetchPRURLForCurrentBranch(ctx context.Context, bins Binaries, workDir, runDir string) (string, error) {
	cmd := runner.Command{
		WorkDir: workDir,
		Argv:    []string{bins.gh(), "pr", "view", "--json", "url", "-q", ".url"},
	}
	result, runErr, writeErr := runCommandEvidence(ctx, cmd, runDir, "gh_pr_view")
	if runErr != nil {
		if isGHPRViewNoPullRequest(result.Stderr) {
			if writeErr != nil {
				return "", writeErr
			}
			return "", nil
		}
		return "", errors.Join(fmt.Errorf("gh pr view failed: %w", runErr), writeErr)
	}
	if writeErr != nil {
		return "", writeErr
	}
	return strings.TrimSpace(result.Stdout), nil
}

var ghPRViewNoPullRequestPattern = regexp.MustCompile(`(?im)^\s*no pull requests found(?:\s+for\s+(?:branch|current branch)\b.*)?\s*$`)

func isGHPRViewNoPullRequest(stderr string) bool {
	return ghPRViewNoPullRequestPattern.MatchString(stderr)
}

func runCommandWithEvidence(ctx context.Context, cmd runner.Command, runDir, label, failure string) (runner.RunResult, error) {
	result, runErr, writeErr := runCommandEvidence(ctx, cmd, runDir, label)
	if runErr != nil {
		return result, errors.Join(fmt.Errorf("%s: %w", failure, runErr), writeErr)
	}
	if writeErr != nil {
		return result, writeErr
	}
	return result, nil
}

func runCommandEvidence(ctx context.Context, cmd runner.Command, runDir, label string) (runner.RunResult, error, error) {
	result, runErr := runner.RunCommand(ctx, cmd, runner.RunOptions{
		StdoutPath: filepath.Join(runDir, label+".stdout.log"),
		StderrPath: filepath.Join(runDir, label+".stderr.log"),
	})
	writeErr := writeJSON(filepath.Join(runDir, label+"_result.json"), result)
	return result, runErr, writeErr
}

// FetchPRComments returns all PR comments using gh api pagination.
func FetchPRComments(ctx context.Context, bins Binaries, root, prURL string) ([]PRComment, error) {
	owner, repo, number, err := ParseGitHubPRURL(prURL)
	if err != nil {
		return nil, err
	}
	apiPath := fmt.Sprintf("repos/%s/%s/issues/%s/comments", owner, repo, number)
	output, err := fetchGHAPIOutput(ctx, bins, root, apiPath, "PR comments", "--paginate", "--slurp")
	if err != nil {
		return nil, err
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
	payload, err := fetchGHAPIJSON[struct {
		User struct {
			Login string `json:"login"`
		} `json:"user"`
	}](ctx, bins, root, apiPath, "PR author")
	if err != nil {
		return "", err
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
	return fetchGHAPIJSON[PullRequestState](ctx, bins, root, apiPath, "PR state")
}

func fetchGHAPIJSON[T any](ctx context.Context, bins Binaries, root, apiPath, label string, extraArgs ...string) (T, error) {
	var zero T
	output, err := fetchGHAPIOutput(ctx, bins, root, apiPath, label, extraArgs...)
	if err != nil {
		return zero, err
	}
	var value T
	if err := json.Unmarshal(output, &value); err != nil {
		return zero, fmt.Errorf("decode %s: %w", label, err)
	}
	return value, nil
}

func fetchGHAPIOutput(ctx context.Context, bins Binaries, root, apiPath, label string, extraArgs ...string) ([]byte, error) {
	stdoutFile, err := os.CreateTemp("", "galley-gh-api-*.json")
	if err != nil {
		return nil, fmt.Errorf("create %s temp file: %w", label, err)
	}
	stdoutPath := stdoutFile.Name()
	if err := stdoutFile.Close(); err != nil {
		return nil, fmt.Errorf("close %s temp file: %w", label, err)
	}
	defer os.Remove(stdoutPath)
	argv := append([]string{bins.gh(), "api", apiPath}, extraArgs...)
	_, err = runner.RunCommand(ctx, runner.Command{
		WorkDir: root,
		Argv:    argv,
	}, runner.RunOptions{StdoutPath: stdoutPath})
	if err != nil {
		return nil, fmt.Errorf("gh api %s failed: %w", label, err)
	}
	output, err := os.ReadFile(stdoutPath)
	if err != nil {
		return nil, fmt.Errorf("read %s response: %w", label, err)
	}
	return output, nil
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
