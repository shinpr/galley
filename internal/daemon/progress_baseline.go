// Package daemon contains progress baseline helpers.
//
// This file owns the post-preflight content-baseline progress detection used
// by runSupervisorLoop. Preflight materializes
// AC-linked skeleton files in the worktree before the executor runs, so every
// attempt would otherwise see those skeleton files in the dirty diff and
// never trigger the no-diff invariant. hasNonSkeletonProgress subtracts the
// post-preflight skeleton files (matched by sha256) from the dirty set so:
//
// - genuine non-skeleton diffs always count as progress;
// - executor edits to skeleton content count as progress (hash mismatch);
// - repeated attempts that leave preflight skeletons unchanged still trip
// the existing consecutive-no-diff escalation.
package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	skeletonpreflight "github.com/shinpr/galley/internal/preflight/skeleton"
	"github.com/shinpr/galley/internal/workspace"
)

// hasNonSkeletonProgress reports whether the workspace snapshot contains any
// change beyond the preflight skeleton baseline. When preflight is absent,
// dirty-diff implies progress as before. When preflight recorded a baseline,
// every changed path that is also a baseline path with matching content is
// excluded; if anything else changed, or any skeleton path differs from
// baseline, the attempt counts as progress.
func hasNonSkeletonProgress(snapshot workspace.Snapshot, workDir string, preflight *skeletonpreflight.Result) (bool, error) {
	if !snapshot.Dirty {
		return false, nil
	}
	changed := changedFilesFromSnapshot(snapshot)
	if len(changed) == 0 {
		// Snapshot reported dirty but porcelain/branch evidence is empty.
		// Fall back to treating that as progress so we never accidentally
		// suppress a genuine change the parser missed.
		return true, nil
	}
	if preflight == nil || len(preflight.Baseline.SkeletonHashes) == 0 {
		return true, nil
	}
	skeletonHashes := make(map[string]string, len(preflight.Baseline.SkeletonHashes))
	for _, h := range preflight.Baseline.SkeletonHashes {
		skeletonHashes[filepath.Clean(h.Path)] = h.SHA256
	}
	for path := range changed {
		clean := filepath.Clean(path)
		expected, isSkeleton := skeletonHashes[clean]
		if !isSkeleton {
			return true, nil
		}
		full := filepath.Join(workDir, clean)
		data, err := os.ReadFile(full)
		if err != nil {
			// Skeleton was deleted or otherwise unreadable: that is an
			// executor-visible change relative to the baseline, count it as
			// progress.
			return true, nil
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != expected {
			return true, nil
		}
	}
	return false, nil
}

// changedFilesFromSnapshot returns the union of branch-committed files (since
// baseSHA) and porcelain-reported uncommitted files. The map keys are the
// worktree-relative paths; values are unused.
func changedFilesFromSnapshot(snapshot workspace.Snapshot) map[string]struct{} {
	out := make(map[string]struct{})
	for _, p := range snapshot.BranchFiles {
		if p != "" {
			out[p] = struct{}{}
		}
	}
	for _, p := range parsePorcelainPaths(snapshot.StatusPorcelain) {
		out[p] = struct{}{}
	}
	return out
}

// parsePorcelainPaths includes both rename endpoints for protection and progress checks.
func parsePorcelainPaths(porcelain string) []string {
	var paths []string
	for _, line := range strings.Split(porcelain, "\n") {
		paths = append(paths, porcelainLinePaths(line)...)
	}
	return paths
}

// addEligiblePorcelainPaths omits staged deletions and staged rename sources.
func addEligiblePorcelainPaths(porcelain string) []string {
	var paths []string
	for _, line := range strings.Split(porcelain, "\n") {
		if len(line) < 4 || isStagedOnlyDeletion(line[0], line[1]) {
			continue
		}
		if changed := porcelainLinePaths(line); len(changed) > 0 {
			paths = append(paths, changed[len(changed)-1])
		}
	}
	return paths
}

func porcelainLinePaths(line string) []string {
	if len(line) < 4 {
		return nil
	}
	body := line[3:]
	var paths []string
	if isRenameOrCopyStatus(line[0]) || isRenameOrCopyStatus(line[1]) {
		separator := strings.Index(body, " -> ")
		if strings.HasPrefix(body, `"`) {
			for i := 1; i < len(body); i++ {
				if body[i] == '\\' {
					i++
					continue
				}
				if body[i] == '"' {
					separator = i + 1
					break
				}
			}
		}
		if separator >= 0 && strings.HasPrefix(body[separator:], " -> ") {
			paths = append(paths, unquoteGitPath(body[:separator]))
			body = body[separator+4:]
		}
	}
	if body != "" {
		paths = append(paths, unquoteGitPath(body))
	}
	return paths
}

func unquoteGitPath(path string) string {
	if strings.HasPrefix(path, `"`) {
		if decoded, err := strconv.Unquote(path); err == nil {
			return decoded
		}
	}
	return path
}
