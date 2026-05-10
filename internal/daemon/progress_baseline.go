// Package daemon — progress baseline helpers.
//
// This file owns the post-preflight content-baseline progress detection used
// by runSupervisorLoop (D5, AC-012, AC-013, AC-014). Preflight materializes
// AC-linked skeleton files in the worktree before the executor runs, so every
// attempt would otherwise see those skeleton files in the dirty diff and
// never trigger the no-diff invariant. hasNonSkeletonProgress subtracts the
// post-preflight skeleton files (matched by sha256) from the dirty set so:
//
//   - genuine non-skeleton diffs always count as progress;
//   - executor edits to skeleton content count as progress (hash mismatch);
//   - repeated attempts that leave preflight skeletons unchanged still trip
//     the existing consecutive-no-diff escalation.
package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"

	"github.com/shinpr/galley/internal/workspace"
)

// hasNonSkeletonProgress reports whether the workspace snapshot contains any
// change beyond the preflight skeleton baseline. When preflight is absent,
// dirty-diff implies progress as before. When preflight recorded a baseline,
// every changed path that is also a baseline path with matching content is
// excluded; if anything else changed, or any skeleton path differs from
// baseline, the attempt counts as progress.
func hasNonSkeletonProgress(snapshot workspace.Snapshot, workDir string, preflight *AcceptanceSkeletonResult) (bool, error) {
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
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			out[trimmed] = struct{}{}
		}
	}
	for _, p := range parsePorcelainPaths(snapshot.StatusPorcelain) {
		out[p] = struct{}{}
	}
	return out
}

// parsePorcelainPaths parses `git status --porcelain` text output (no -z) and
// returns the list of paths it reports as changed. Renames (R/C) are encoded
// as "old -> new"; only the new path is returned.
func parsePorcelainPaths(porcelain string) []string {
	if porcelain == "" {
		return nil
	}
	var paths []string
	for _, raw := range strings.Split(porcelain, "\n") {
		line := strings.TrimRight(raw, "\r")
		if len(line) < 4 {
			continue
		}
		// Porcelain format: XY<space>path[ -> renamed]
		body := line[3:]
		if idx := strings.Index(body, " -> "); idx >= 0 {
			body = body[idx+4:]
		}
		body = strings.TrimSpace(body)
		// Quoted paths may appear when filenames contain unusual characters.
		body = strings.Trim(body, `"`)
		if body == "" {
			continue
		}
		paths = append(paths, body)
	}
	return paths
}
