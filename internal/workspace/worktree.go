package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/shinpr/galley/internal/task"
)

// ErrDirtyWorktree indicates cleanup was skipped because the worktree has local changes.
var ErrDirtyWorktree = errors.New("dirty worktree")

// Snapshot records git evidence for a prepared workspace.
type Snapshot struct {
	CWD             string `json:"cwd"`
	StatusPorcelain string `json:"status_porcelain"`
	Diff            string `json:"diff"`
	Dirty           bool   `json:"dirty"`
}

// Prepared describes the workspace path selected for execution.
type Prepared struct {
	CWD             string `json:"cwd"`
	WorktreeCreated bool   `json:"worktree_created"`
	WorktreeReused  bool   `json:"worktree_reused"`
	Dirty           bool   `json:"dirty"`
	StatusPorcelain string `json:"status_porcelain,omitempty"`
	Branch          string `json:"branch,omitempty"`
	Path            string `json:"path,omitempty"`
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
func Prepare(ctx context.Context, sourceCWD string, spec task.Worktree) (Prepared, error) {
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
		return reuseExistingWorktree(ctx, worktreePath, spec.Branch)
	} else if !os.IsNotExist(err) {
		return Prepared{}, fmt.Errorf("inspect worktree path %s: %w", worktreePath, err)
	}
	if err := os.MkdirAll(filepath.Dir(worktreePath), 0o755); err != nil {
		return Prepared{}, fmt.Errorf("create worktree parent %s: %w", filepath.Dir(worktreePath), err)
	}

	args := []string{"-C", sourceCWD, "worktree", "add"}
	if branchExists(ctx, sourceCWD, spec.Branch) {
		args = append(args, worktreePath, spec.Branch)
	} else {
		args = append(args, "-b", spec.Branch, worktreePath)
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return Prepared{}, fmt.Errorf("git worktree add: %w: %s", err, string(output))
	}
	status, dirty := statusPorcelain(ctx, worktreePath)
	return Prepared{
		CWD:             worktreePath,
		WorktreeCreated: true,
		Dirty:           dirty,
		StatusPorcelain: status,
		Branch:          spec.Branch,
		Path:            worktreePath,
	}, nil
}

func reuseExistingWorktree(ctx context.Context, worktreePath, branch string) (Prepared, error) {
	inside, err := gitOutput(ctx, worktreePath, "rev-parse", "--is-inside-work-tree")
	if err != nil || inside != "true" {
		return Prepared{}, fmt.Errorf("worktree path already exists and is not a git worktree: %s", worktreePath)
	}
	currentBranch, err := gitOutput(ctx, worktreePath, "branch", "--show-current")
	if err != nil {
		return Prepared{}, fmt.Errorf("inspect worktree branch %s: %w", worktreePath, err)
	}
	if currentBranch != branch {
		return Prepared{}, fmt.Errorf("worktree path %s is on branch %q, want %q", worktreePath, currentBranch, branch)
	}
	status, dirty := statusPorcelain(ctx, worktreePath)
	return Prepared{
		CWD:             worktreePath,
		WorktreeReused:  true,
		Dirty:           dirty,
		StatusPorcelain: status,
		Branch:          branch,
		Path:            worktreePath,
	}, nil
}

func branchExists(ctx context.Context, sourceCWD, branch string) bool {
	cmd := exec.CommandContext(ctx, "git", "-C", sourceCWD, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	return cmd.Run() == nil
}

func gitOutput(ctx context.Context, cwd string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = cwd
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(bytes.TrimSpace(output)), nil
}

func statusPorcelain(ctx context.Context, cwd string) (string, bool) {
	status, err := gitOutput(ctx, cwd, "status", "--porcelain")
	if err != nil {
		return "", false
	}
	return status, status != ""
}

// CaptureSnapshot captures status and diff evidence for supervisor review.
func CaptureSnapshot(ctx context.Context, cwd string) (Snapshot, error) {
	status, err := gitOutput(ctx, cwd, "status", "--porcelain")
	if err != nil {
		return Snapshot{}, fmt.Errorf("git status: %w", err)
	}
	diff, err := gitOutput(ctx, cwd, "diff", "--binary")
	if err != nil {
		return Snapshot{}, fmt.Errorf("git diff: %w", err)
	}
	return Snapshot{
		CWD:             cwd,
		StatusPorcelain: status,
		Diff:            diff,
		Dirty:           status != "",
	}, nil
}

// Remove removes a clean git worktree for a completed task.
func Remove(ctx context.Context, sourceCWD string, spec task.Worktree) (CleanupResult, error) {
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
	status, dirty := statusPorcelain(ctx, worktreePath)
	result.StatusPorcelain = status
	result.Dirty = dirty
	if dirty {
		return result, fmt.Errorf("%w: %s", ErrDirtyWorktree, worktreePath)
	}
	cmd := exec.CommandContext(ctx, "git", "-C", sourceCWD, "worktree", "remove", worktreePath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return result, fmt.Errorf("git worktree remove: %w: %s", err, string(output))
	}
	result.Removed = true
	return result, nil
}
