package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/shinpr/galley/internal/runner"
	"github.com/shinpr/galley/internal/task"
)

// ErrDirtyWorktree indicates cleanup was skipped because the worktree has local changes.
var ErrDirtyWorktree = errors.New("dirty worktree")

// Snapshot records git evidence for a prepared workspace.
type Snapshot struct {
	CWD             string   `json:"cwd"`
	BaseSHA         string   `json:"base_sha,omitempty"`
	HeadSHA         string   `json:"head_sha,omitempty"`
	StatusPorcelain string   `json:"status_porcelain"`
	BranchDiff      string   `json:"branch_diff,omitempty"`
	BranchFiles     []string `json:"branch_files,omitempty"`
	StagedDiff      string   `json:"staged_diff,omitempty"`
	UnstagedDiff    string   `json:"unstaged_diff,omitempty"`
	Diff            string   `json:"diff"`
	Dirty           bool     `json:"dirty"`
}

// Prepared describes the workspace path selected for execution.
type Prepared struct {
	CWD             string `json:"cwd"`
	WorktreeCreated bool   `json:"worktree_created"`
	WorktreeReused  bool   `json:"worktree_reused"`
	Dirty           bool   `json:"dirty"`
	StatusPorcelain string `json:"status_porcelain,omitempty"`
	BaseSHA         string `json:"base_sha,omitempty"`
	Branch          string `json:"branch,omitempty"`
	Path            string `json:"path,omitempty"`
}

// Options controls git execution for workspace operations.
type Options struct {
	GitBin string
	// StartPoint is the git ref name to use as the start-point when Prepare
	// creates a brand-new task branch (the `git worktree add -b <branch>
	// <path> <start-point>` form). When empty, Prepare omits the trailing
	// positional argument and `git worktree add` falls back to the source
	// repository's current HEAD. StartPoint is intentionally ignored when
	// Prepare reuses an existing worktree path or attaches to an existing
	// branch, because re-pointing a materialized branch from a different ref
	// would silently rewrite its history.
	StartPoint string
}

func (opts Options) git() string {
	if opts.GitBin != "" {
		return opts.GitBin
	}
	return "git"
}

// CleanupResult records the result of a worktree cleanup attempt.
type CleanupResult struct {
	Path            string `json:"path"`
	Removed         bool   `json:"removed"`
	AlreadyMissing  bool   `json:"already_missing,omitempty"`
	Dirty           bool   `json:"dirty,omitempty"`
	StatusPorcelain string `json:"status_porcelain,omitempty"`
}

// Prepare creates an isolated git worktree when the task requests one.
func Prepare(ctx context.Context, sourceCWD string, spec task.Worktree, opts Options) (Prepared, error) {
	if !spec.Enabled {
		return Prepared{CWD: sourceCWD}, nil
	}
	if spec.Branch == "" {
		return Prepared{}, fmt.Errorf("worktree branch is required")
	}
	if spec.Path == "" {
		return Prepared{}, fmt.Errorf("worktree path is required")
	}

	worktreePath := spec.Path
	if !filepath.IsAbs(worktreePath) {
		worktreePath = filepath.Join(sourceCWD, worktreePath)
	}
	worktreePath = filepath.Clean(worktreePath)
	if _, err := os.Stat(worktreePath); err == nil {
		return reuseExistingWorktree(ctx, worktreePath, spec.Branch, opts)
	} else if !os.IsNotExist(err) {
		return Prepared{}, fmt.Errorf("inspect worktree path %s: %w", worktreePath, err)
	}
	if err := os.MkdirAll(filepath.Dir(worktreePath), 0o755); err != nil {
		return Prepared{}, fmt.Errorf("create worktree parent %s: %w", filepath.Dir(worktreePath), err)
	}

	args := []string{"-C", sourceCWD, "worktree", "add"}
	exists, err := branchExists(ctx, sourceCWD, spec.Branch, opts)
	if err != nil {
		return Prepared{}, err
	}
	if exists {
		// The branch already exists; reuse its tip. Applying StartPoint here
		// would re-point an existing branch and silently rewrite history.
		args = append(args, worktreePath, spec.Branch)
	} else {
		args = append(args, "-b", spec.Branch, worktreePath)
		if opts.StartPoint != "" {
			args = append(args, opts.StartPoint)
		}
	}
	result, err := runGitCommand(ctx, opts, "", args...)
	if err != nil {
		return Prepared{}, fmt.Errorf("git worktree add: %w: %s", err, strings.TrimSpace(result.Stderr))
	}
	status, dirty, err := statusPorcelain(ctx, worktreePath, opts)
	if err != nil {
		return Prepared{}, err
	}
	baseSHA, err := headSHA(ctx, worktreePath, opts)
	if err != nil {
		return Prepared{}, fmt.Errorf("git rev-parse HEAD for worktree %s: %w", worktreePath, err)
	}
	return Prepared{
		CWD:             worktreePath,
		WorktreeCreated: true,
		Dirty:           dirty,
		StatusPorcelain: status,
		BaseSHA:         baseSHA,
		Branch:          spec.Branch,
		Path:            worktreePath,
	}, nil
}

func reuseExistingWorktree(ctx context.Context, worktreePath, branch string, opts Options) (Prepared, error) {
	inside, err := gitOutput(ctx, opts, worktreePath, "rev-parse", "--is-inside-work-tree")
	if err != nil || inside != "true" {
		return Prepared{}, fmt.Errorf("worktree path already exists and is not a git worktree: %s", worktreePath)
	}
	currentBranch, err := gitOutput(ctx, opts, worktreePath, "branch", "--show-current")
	if err != nil {
		return Prepared{}, fmt.Errorf("inspect worktree branch %s: %w", worktreePath, err)
	}
	if currentBranch != branch {
		return Prepared{}, fmt.Errorf("worktree path %s is on branch %q, want %q", worktreePath, currentBranch, branch)
	}
	status, dirty, err := statusPorcelain(ctx, worktreePath, opts)
	if err != nil {
		return Prepared{}, err
	}
	baseSHA, err := headSHA(ctx, worktreePath, opts)
	if err != nil {
		return Prepared{}, fmt.Errorf("git rev-parse HEAD for worktree %s: %w", worktreePath, err)
	}
	return Prepared{
		CWD:             worktreePath,
		WorktreeReused:  true,
		Dirty:           dirty,
		StatusPorcelain: status,
		BaseSHA:         baseSHA,
		Branch:          branch,
		Path:            worktreePath,
	}, nil
}

func headSHA(ctx context.Context, cwd string, opts Options) (string, error) {
	return gitOutput(ctx, opts, cwd, "rev-parse", "HEAD")
}

func branchExists(ctx context.Context, sourceCWD, branch string, opts Options) (bool, error) {
	result, err := runGitCommand(ctx, opts, "", "-C", sourceCWD, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	if err != nil {
		if result.ExitCode == 1 {
			return false, nil
		}
		return false, fmt.Errorf("git show-ref branch %s: %w: %s", branch, err, strings.TrimSpace(result.Stderr))
	}
	return true, nil
}

func gitOutput(ctx context.Context, opts Options, cwd string, args ...string) (string, error) {
	result, err := runGitCommand(ctx, opts, cwd, args...)
	if err != nil {
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(result.Stderr))
	}
	return string(bytes.TrimSpace([]byte(result.Stdout))), nil
}

func statusPorcelain(ctx context.Context, cwd string, opts Options) (string, bool, error) {
	status, err := gitOutput(ctx, opts, cwd, "status", "--porcelain")
	if err != nil {
		return "", false, fmt.Errorf("git status porcelain %s: %w", cwd, err)
	}
	return status, status != "", nil
}

// CaptureSnapshot captures status and diff evidence for supervisor review.
func CaptureSnapshot(ctx context.Context, cwd string, opts Options) (Snapshot, error) {
	return CaptureSnapshotFromBase(ctx, cwd, "", opts)
}

// CaptureSnapshotFromBase captures committed, staged, and unstaged changes for supervisor review.
func CaptureSnapshotFromBase(ctx context.Context, cwd, baseSHA string, opts Options) (Snapshot, error) {
	status, err := gitOutput(ctx, opts, cwd, "status", "--porcelain")
	if err != nil {
		return Snapshot{}, fmt.Errorf("git status: %w", err)
	}
	head, err := headSHA(ctx, cwd, opts)
	if err != nil {
		return Snapshot{}, fmt.Errorf("git rev-parse HEAD: %w", err)
	}
	branchDiff := ""
	var branchFiles []string
	if baseSHA != "" && baseSHA != head {
		branchDiff, err = gitOutput(ctx, opts, cwd, "diff", "--binary", baseSHA+"..HEAD")
		if err != nil {
			return Snapshot{}, fmt.Errorf("git branch diff: %w", err)
		}
		branchFiles, err = gitChangedFiles(ctx, cwd, baseSHA+"..HEAD", opts)
		if err != nil {
			return Snapshot{}, fmt.Errorf("git branch changed files: %w", err)
		}
	}
	stagedDiff, err := gitOutput(ctx, opts, cwd, "diff", "--cached", "--binary")
	if err != nil {
		return Snapshot{}, fmt.Errorf("git staged diff: %w", err)
	}
	unstagedDiff, err := gitOutput(ctx, opts, cwd, "diff", "--binary")
	if err != nil {
		return Snapshot{}, fmt.Errorf("git diff: %w", err)
	}
	diff := joinDiffs(branchDiff, stagedDiff, unstagedDiff)
	return Snapshot{
		CWD:             cwd,
		BaseSHA:         baseSHA,
		HeadSHA:         head,
		StatusPorcelain: status,
		BranchDiff:      branchDiff,
		BranchFiles:     branchFiles,
		StagedDiff:      stagedDiff,
		UnstagedDiff:    unstagedDiff,
		Diff:            diff,
		Dirty:           status != "" || branchDiff != "",
	}, nil
}

func gitChangedFiles(ctx context.Context, cwd, revision string, opts Options) ([]string, error) {
	result, err := runGitCommand(ctx, opts, cwd, "diff", "--name-only", "-z", revision)
	if err != nil {
		return nil, err
	}
	output := []byte(result.Stdout)
	if len(output) == 0 {
		return nil, nil
	}
	parts := bytes.Split(bytes.TrimRight(output, "\x00"), []byte{0})
	files := make([]string, 0, len(parts))
	for _, part := range parts {
		if len(part) > 0 {
			files = append(files, string(part))
		}
	}
	return files, nil
}

func joinDiffs(parts ...string) string {
	var joined []string
	for _, part := range parts {
		if part != "" {
			joined = append(joined, part)
		}
	}
	return strings.Join(joined, "\n")
}

// Remove removes a clean git worktree for a completed task.
func Remove(ctx context.Context, sourceCWD string, spec task.Worktree, opts Options) (CleanupResult, error) {
	if !spec.Enabled || spec.Path == "" {
		return CleanupResult{}, nil
	}
	worktreePath := spec.Path
	if !filepath.IsAbs(worktreePath) {
		worktreePath = filepath.Join(sourceCWD, worktreePath)
	}
	worktreePath = filepath.Clean(worktreePath)
	result := CleanupResult{Path: worktreePath}
	if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
		result.AlreadyMissing = true
		return result, nil
	} else if err != nil {
		return result, fmt.Errorf("inspect worktree path %s: %w", worktreePath, err)
	}
	status, dirty, err := statusPorcelain(ctx, worktreePath, opts)
	if err != nil {
		return result, err
	}
	result.StatusPorcelain = status
	result.Dirty = dirty
	if dirty {
		return result, fmt.Errorf("%w: %s", ErrDirtyWorktree, worktreePath)
	}
	gitResult, err := runGitCommand(ctx, opts, "", "-C", sourceCWD, "worktree", "remove", worktreePath)
	if err != nil {
		return result, fmt.Errorf("git worktree remove: %w: %s", err, strings.TrimSpace(gitResult.Stderr))
	}
	result.Removed = true
	return result, nil
}

func runGitCommand(ctx context.Context, opts Options, cwd string, args ...string) (runner.RunResult, error) {
	argv := append([]string{opts.git()}, args...)
	return runner.RunCommand(ctx, runner.Command{WorkDir: cwd, Argv: argv}, runner.RunOptions{TailBytes: -1})
}
