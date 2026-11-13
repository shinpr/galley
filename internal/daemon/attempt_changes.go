package daemon

import (
	"path/filepath"

	skeletonpreflight "github.com/shinpr/galley/internal/preflight/skeleton"
)

// attemptChangedFiles lists in slash form the files that differ between the
// pre-attempt and post-attempt worktree snapshots.
func attemptChangedFiles(before, after skeletonpreflight.WorktreeSnapshot) *[]string {
	changed := skeletonpreflight.ChangedWorktreeFiles(before, after)
	for i, path := range changed {
		changed[i] = filepath.ToSlash(path)
	}
	return &changed
}
