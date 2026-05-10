package daemon

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/shinpr/galley/internal/task"
)

// reconcileReusedInputFiles makes a reused task worktree safe for
// inputfiles.Prepare, which refuses to overwrite existing destinations. A
// context-only (commit=false) input file left behind by a prior run on the same
// worktree is removed when its content already matches the task input source, so
// Prepare can re-copy it cleanly. A destination whose content conflicts with the
// task input source is reported as a failure with clear evidence so an operator
// can resolve it. Committed input files are left for inputfiles.Prepare so its
// precise git-aware checks still apply.
//
// Before reading or removing any destination, the location is resolved through
// containedReusedDestination, which mirrors the containment rules
// inputfiles.Prepare uses (local relative path, existing parent resolves inside
// the worktree after symlink evaluation). When that check fails the destination
// is skipped here and left for inputfiles.Prepare to reject with a precise
// message, so a symlinked destination parent can never redirect a read or
// remove outside the worktree.
func reconcileReusedInputFiles(workDir string, files []task.InputFile) error {
	for i, file := range files {
		if file.Commit || file.Source == "" || file.Destination == "" {
			continue
		}
		dst, err := containedReusedDestination(workDir, file.Destination)
		if err != nil {
			// A non-local destination, or one whose parent escapes the worktree
			// (e.g. via a symlink), is left for inputfiles.Prepare to reject with
			// a precise validation error; never read or remove through it here.
			continue
		}
		existing, err := os.ReadFile(dst)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("files[%d]: inspect reused worktree destination %s: %w", i, dst, err)
		}
		source, err := os.ReadFile(file.Source)
		if err != nil {
			return fmt.Errorf("files[%d]: read task input source %s: %w", i, file.Source, err)
		}
		if bytes.Equal(existing, source) {
			if err := os.Remove(dst); err != nil {
				return fmt.Errorf("files[%d]: refresh reused worktree destination %s: %w", i, dst, err)
			}
			continue
		}
		return fmt.Errorf("files[%d]: reused worktree has a conflicting file at %s; existing content differs from task input source %s", i, file.Destination, file.Source)
	}
	return nil
}

// containedReusedDestination resolves dst under workDir using the same
// containment rules inputfiles.Prepare applies before touching a destination:
// dst must be a local relative path, and the nearest existing ancestor of its
// parent directory must resolve inside workDir after symlink evaluation. It
// returns the joined destination path when the location is safe to read from or
// remove. On a containment error the caller defers to inputfiles.Prepare for the
// precise validation message rather than reading or removing through the
// escaping path.
func containedReusedDestination(workDir, dst string) (string, error) {
	if dst == "" {
		return "", fmt.Errorf("empty path")
	}
	if !filepath.IsLocal(dst) {
		return "", fmt.Errorf("must be a local relative path: %s", dst)
	}
	resolved := filepath.Join(workDir, filepath.Clean(dst))
	if err := ensureExistingParentUnderRoot(workDir, filepath.Dir(resolved)); err != nil {
		return "", err
	}
	return resolved, nil
}

// ensureExistingParentUnderRoot walks dir upward to its nearest existing
// ancestor and verifies that ancestor resolves inside root after symlink
// evaluation, so a symlinked parent cannot redirect an operation outside root.
func ensureExistingParentUnderRoot(root, dir string) error {
	for {
		if _, err := os.Stat(dir); err == nil {
			return ensureUnderRoot(root, dir)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("inspect destination parent %s: %w", dir, err)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return fmt.Errorf("destination parent has no existing ancestor: %s", dir)
		}
		dir = parent
	}
}

func ensureUnderRoot(root, path string) error {
	rootReal, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("resolve workdir %s: %w", root, err)
	}
	pathReal, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("resolve destination parent %s: %w", path, err)
	}
	rel, err := filepath.Rel(rootReal, pathReal)
	if err != nil {
		return fmt.Errorf("compare destination parent %s to workdir %s: %w", path, root, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("destination parent resolves outside workdir: %s", path)
	}
	return nil
}
