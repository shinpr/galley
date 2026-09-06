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
	"github.com/shinpr/galley/internal/proc"
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

// Repo names the working tree a git or gh command runs in, its binaries, and
// the run directory for evidence. RunDir is empty when no evidence is written.
type Repo struct {
	Bins    Binaries
	WorkDir string
	RunDir  string
}

// PullRequestSpec is the content of the pull request Galley opens.
type PullRequestSpec struct {
	Title    string
	BodyPath string
	Base     string
}

// ghAPIRequest is one `gh api` call: the API path, the label used in error and
// evidence text, and any extra gh arguments.
type ghAPIRequest struct {
	Path      string
	Label     string
	ExtraArgs []string
}

// commandEvidence names where a command's evidence files land and how its
// failure is described.
type commandEvidence struct {
	RunDir  string
	Label   string
	Failure string
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

// AddPaths stages literal worktree paths, using stdin on Windows and for large sets.
func AddPaths(ctx context.Context, repo Repo, paths []string) error {
	return addPathsForOS(ctx, repo, paths, runtime.GOOS)
}

func addPathsForOS(ctx context.Context, repo Repo, paths []string, goos string) error {
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
	cmd := proc.Command{WorkDir: repo.WorkDir}
	if goos == "windows" || len(strings.Join(pathspecs, "\x00")) > 32*1024 {
		// Bound argv on every OS and preserve literal filename bytes via NUL separators.
		cmd.Argv = proc.GitArgs(repo.Bins.git(), "add", "-A", "--pathspec-from-file=-", "--pathspec-file-nul")
		cmd.Stdin = strings.Join(pathspecs, "\x00") + "\x00"
	} else {
		cmd.Argv = proc.GitArgs(repo.Bins.git(), append([]string{"add", "-A", "--"}, pathspecs...)...)
	}
	_, err := runCommandWithEvidence(ctx, cmd, commandEvidence{RunDir: repo.RunDir, Label: "git_add", Failure: "git add failed"})
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
func StatusPorcelainZ(ctx context.Context, repo Repo) (string, error) {
	result, err := proc.RunVCSCommand(ctx, proc.Command{
		WorkDir: repo.WorkDir,
		Argv:    proc.GitArgs(repo.Bins.git(), "status", "--porcelain=v1", "-z", "--untracked-files=all"),
	}, proc.RunOptions{TailBytes: -1})
	if err != nil {
		return "", fmt.Errorf("git status --porcelain -z (review staging discovery) failed: %w", err)
	}
	return result.Stdout, nil
}

// StagePathsForReview stages only literal executor-produced paths and records
// skipped or completed staging evidence for the supervisor.
func StagePathsForReview(ctx context.Context, repo Repo, paths []string) error {
	return stagePathsForReviewForOS(ctx, repo, paths, runtime.GOOS)
}

func stagePathsForReviewForOS(ctx context.Context, repo Repo, paths []string, goos string) error {
	stagePaths := dedupeReviewPaths(paths)
	if len(stagePaths) == 0 {
		// The executor produced no reviewable change. Persist a skipped
		// evidence payload so reviewers can still confirm review staging
		// ran for this attempt and then return without calling
		// git add at all. Without this short-circuit, the alternative
		// `git add -A` over the whole worktree would re-stage every
		// dirty path (including context-only commit:false inputs),
		// defeating the explicit reviewable-set contract.
		return writeReviewStagingSkipped(repo.RunDir, "no executor-produced paths to stage")
	}
	pathspecs := literalPathspecs(stagePaths)
	cmd := proc.Command{WorkDir: repo.WorkDir}
	if goos == "windows" || len(strings.Join(pathspecs, "\x00")) > 32*1024 {
		// Large reviews use the same literal stdin pathspec protocol as finalization.
		cmd.Argv = proc.GitArgs(repo.Bins.git(), "add", "-A", "--pathspec-from-file=-", "--pathspec-file-nul")
		cmd.Stdin = strings.Join(pathspecs, "\x00") + "\x00"
	} else {
		cmd.Argv = proc.GitArgs(repo.Bins.git(), append([]string{"add", "-A", "--"}, pathspecs...)...)
	}
	result, err := proc.RunVCSCommand(ctx, cmd, proc.RunOptions{
		StdoutPath: filepath.Join(repo.RunDir, "git_add_review.stdout.log"),
		StderrPath: filepath.Join(repo.RunDir, "git_add_review.stderr.log"),
	})
	writeErr := writeJSON(filepath.Join(repo.RunDir, "git_add_review_result.json"), result)
	if err != nil {
		return errors.Join(fmt.Errorf("git add -A (review staging) failed: %w", err), writeErr)
	}
	if writeErr != nil {
		return writeErr
	}
	return nil
}

// writeReviewStagingSkipped records why no executor-produced path was staged.
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

// IsAncestor reports whether ancestor is reachable from descendant in workDir.
// Exit code 0 is yes, 1 is no, and any other exit code is an error.
func IsAncestor(ctx context.Context, repo Repo, ancestor, descendant string) (bool, error) {
	result, err := proc.RunVCSCommand(ctx, proc.Command{
		WorkDir: repo.WorkDir,
		Argv:    proc.GitArgs(repo.Bins.git(), "merge-base", "--is-ancestor", ancestor, descendant),
	}, proc.RunOptions{})
	if err == nil {
		return true, nil
	}
	if result.ExitCode == 1 {
		return false, nil
	}
	return false, fmt.Errorf("git merge-base --is-ancestor %s %s failed: %w: %s", ancestor, descendant, err, strings.TrimSpace(result.Stderr))
}

// Commit creates a git commit and writes command evidence.
func Commit(ctx context.Context, repo Repo, message string) error {
	_, err := runCommandWithEvidence(ctx, proc.Command{
		WorkDir: repo.WorkDir,
		Argv:    proc.GitArgs(repo.Bins.git(), "commit", "-m", message),
	}, commandEvidence{RunDir: repo.RunDir, Label: "git_commit", Failure: "git commit failed"})
	return err
}

// PushCurrentBranch pushes HEAD to origin and writes command evidence.
func PushCurrentBranch(ctx context.Context, repo Repo) error {
	_, err := runCommandWithEvidence(ctx, proc.Command{
		WorkDir: repo.WorkDir,
		Argv:    proc.GitArgs(repo.Bins.git(), "push", "-u", "origin", "HEAD"),
	}, commandEvidence{RunDir: repo.RunDir, Label: "git_push", Failure: "git push failed"})
	return err
}

// CreatePullRequest opens a GitHub PR with gh and writes command evidence.
func CreatePullRequest(ctx context.Context, repo Repo, spec PullRequestSpec) (string, error) {
	absoluteBodyPath, err := filepath.Abs(spec.BodyPath)
	if err != nil {
		return "", fmt.Errorf("resolve pr body path: %w", err)
	}
	argv := []string{repo.Bins.gh(), "pr", "create", "--title", spec.Title, "--body-file", absoluteBodyPath}
	if spec.Base != "" {
		argv = append(argv, "--base", spec.Base)
	}
	result, err := runCommandWithEvidence(ctx, proc.Command{
		WorkDir: repo.WorkDir,
		Argv:    argv,
	}, commandEvidence{RunDir: repo.RunDir, Label: "gh_pr_create", Failure: "gh pr create failed"})
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
func FetchPRURLForCurrentBranch(ctx context.Context, repo Repo) (string, error) {
	cmd := proc.Command{
		WorkDir: repo.WorkDir,
		Argv:    []string{repo.Bins.gh(), "pr", "view", "--json", "url", "-q", ".url"},
	}
	result, runErr, writeErr := runCommandEvidence(ctx, cmd, repo.RunDir, "gh_pr_view")
	if runErr != nil && !isGHPRViewNoPullRequest(result.Stderr) {
		return "", errors.Join(fmt.Errorf("gh pr view failed: %w", runErr), writeErr)
	}
	if runErr != nil {
		// No open PR for this branch: recover with an empty URL, not an error.
		return "", writeErr
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

func runCommandWithEvidence(ctx context.Context, cmd proc.Command, ev commandEvidence) (proc.RunResult, error) {
	result, runErr, writeErr := runCommandEvidence(ctx, cmd, ev.RunDir, ev.Label)
	if runErr != nil {
		return result, errors.Join(fmt.Errorf("%s: %w", ev.Failure, runErr), writeErr)
	}
	if writeErr != nil {
		return result, writeErr
	}
	return result, nil
}

func runCommandEvidence(ctx context.Context, cmd proc.Command, runDir, label string) (proc.RunResult, error, error) {
	result, runErr := proc.RunVCSCommand(ctx, cmd, proc.RunOptions{
		StdoutPath: filepath.Join(runDir, label+".stdout.log"),
		StderrPath: filepath.Join(runDir, label+".stderr.log"),
	})
	writeErr := writeJSON(filepath.Join(runDir, label+"_result.json"), result)
	return result, runErr, writeErr
}

// FetchPRComments returns all PR comments using gh api pagination.
func FetchPRComments(ctx context.Context, repo Repo, prURL string) ([]PRComment, error) {
	owner, repoName, number, err := ParseGitHubPRURL(prURL)
	if err != nil {
		return nil, err
	}
	apiPath := fmt.Sprintf("repos/%s/%s/issues/%s/comments", owner, repoName, number)
	output, err := fetchGHAPIOutput(ctx, repo, ghAPIRequest{Path: apiPath, Label: "PR comments", ExtraArgs: []string{"--paginate", "--slurp"}})
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
func PostPRComment(ctx context.Context, repo Repo, prURL, body string) error {
	owner, repoName, number, err := ParseGitHubPRURL(prURL)
	if err != nil {
		return err
	}
	apiPath := fmt.Sprintf("repos/%s/%s/issues/%s/comments", owner, repoName, number)
	payload, err := json.Marshal(map[string]string{"body": body})
	if err != nil {
		return fmt.Errorf("encode PR comment body: %w", err)
	}
	_, err = proc.RunVCSCommand(ctx, proc.Command{
		WorkDir: repo.WorkDir,
		Argv:    []string{repo.Bins.gh(), "api", "-X", "POST", apiPath, "--input", "-"},
		Stdin:   string(payload),
	}, proc.RunOptions{})
	if err != nil {
		return fmt.Errorf("gh api post PR comment failed: %w", err)
	}
	return nil
}

// FetchPRAuthorLogin returns the GitHub login of the user who opened the PR.
// Galley persists the result on the task PR record at PR creation time so
// later PR comment authorization can verify the comment author matches the
// PR author without re-fetching from GitHub.
func FetchPRAuthorLogin(ctx context.Context, repo Repo, prURL string) (string, error) {
	owner, repoName, number, err := ParseGitHubPRURL(prURL)
	if err != nil {
		return "", err
	}
	apiPath := fmt.Sprintf("repos/%s/%s/pulls/%s", owner, repoName, number)
	payload, err := fetchGHAPIJSON[struct {
		User struct {
			Login string `json:"login"`
		} `json:"user"`
	}](ctx, repo, ghAPIRequest{Path: apiPath, Label: "PR author"})
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
func FetchPRState(ctx context.Context, repo Repo, prURL string) (PullRequestState, error) {
	owner, repoName, number, err := ParseGitHubPRURL(prURL)
	if err != nil {
		return PullRequestState{}, err
	}
	apiPath := fmt.Sprintf("repos/%s/%s/pulls/%s", owner, repoName, number)
	return fetchGHAPIJSON[PullRequestState](ctx, repo, ghAPIRequest{Path: apiPath, Label: "PR state"})
}

func fetchGHAPIJSON[T any](ctx context.Context, repo Repo, req ghAPIRequest) (T, error) {
	var zero T
	output, err := fetchGHAPIOutput(ctx, repo, req)
	if err != nil {
		return zero, err
	}
	var value T
	if err := json.Unmarshal(output, &value); err != nil {
		return zero, fmt.Errorf("decode %s: %w", req.Label, err)
	}
	return value, nil
}

func fetchGHAPIOutput(ctx context.Context, repo Repo, req ghAPIRequest) ([]byte, error) {
	stdoutFile, err := os.CreateTemp("", "galley-gh-api-*.json")
	if err != nil {
		return nil, fmt.Errorf("create %s temp file: %w", req.Label, err)
	}
	stdoutPath := stdoutFile.Name()
	if err := stdoutFile.Close(); err != nil {
		return nil, fmt.Errorf("close %s temp file: %w", req.Label, err)
	}
	defer func() { _ = os.Remove(stdoutPath) }()
	argv := append([]string{repo.Bins.gh(), "api", req.Path}, req.ExtraArgs...)
	_, err = proc.RunVCSCommand(ctx, proc.Command{
		WorkDir: repo.WorkDir,
		Argv:    argv,
	}, proc.RunOptions{StdoutPath: stdoutPath})
	if err != nil {
		return nil, fmt.Errorf("gh api %s failed: %w", req.Label, err)
	}
	output, err := os.ReadFile(stdoutPath)
	if err != nil {
		return nil, fmt.Errorf("read %s response: %w", req.Label, err)
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
		return nil, fmt.Errorf("decode PR comments: %w", err)
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
