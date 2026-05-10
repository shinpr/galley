package daemon

import (
	"bytes"
	"errors"
	"fmt"
	"os"

	"github.com/shinpr/galley/internal/inputfiles"
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
// inputfiles.ContainedDestination, which applies the same containment rules
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
		dst, err := inputfiles.ContainedDestination(workDir, file.Destination)
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
