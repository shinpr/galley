// Package daemon contains the review-staging path set builder.
//
// This file owns the explicit "reviewable path set" Galley computes after an
// executor attempt and before it captures the snapshot it hands to the
// supervisor. The path set is the daemon-side representation of the
// submitted artifact set: only the worktree paths that belong to the
// executor-produced diff are staged for review. Context-only Galley material
// (task.files entries declared with commit:false, and any other untracked
// content that is not part of the executor's submitted change set) is
// intentionally kept out of the path set so the supervisor diff/evidence and
// downstream progress signals reflect only what the executor actually
// submitted.
//
// Forbidden-path entries are intentionally kept in the path set so the
// existing finalize-time forbidden_paths gate still observes them; review
// staging only excludes context-only Galley material, not safety-gated
// paths.
package daemon

import (
	"path/filepath"
	"strings"

	"github.com/shinpr/galley/internal/task"
)

// reviewablePathsFromStatus extracts the worktree-relative paths Galley
// should stage for supervisor review from the raw output of
// `git status --porcelain=v1 -z` captured after the executor attempt.
//
// Each NUL-terminated entry is parsed into a single reviewable path (the new
// path for renames/copies, the only path otherwise), normalized to slash
// form, deduplicated, and filtered. The function drops:
//
// - empty entries and entries that normalize to ".";
// - non-local entries (filepath.IsLocal rejects absolute paths,
// drive-letter paths, and any segment that backs out of cwd so the
// review-staging set cannot widen beyond the executor's working
// directory regardless of how git status formatted the entry);
// - staged-only deletions, which are already visible in the staged diff and
// would fail if re-added. Unstaged deletions are intentionally kept so review
// staging still stages them;
// - destinations in excludeDestinations — these are task.files entries
// declared with commit:false, which Galley materializes as context-only
// inputs the executor reads but does not submit.
//
// The order of returned paths follows the first occurrence in the porcelain
// stream so the staging command argv is deterministic across runs of the
// same input.
func reviewablePathsFromStatus(statusZ string, excludeDestinations []string) []string {
	excludes := normalizedPathSet(excludeDestinations)
	raw := parseStatusPorcelainZ(statusZ)
	seen := make(map[string]bool, len(raw))
	var result []string
	for _, entry := range raw {
		if isStagedOnlyDeletion(entry.X, entry.Y) {
			continue
		}
		clean := normalizeReviewablePath(entry.Path)
		if clean == "" {
			continue
		}
		if !filepath.IsLocal(clean) {
			continue
		}
		if excludes[clean] {
			continue
		}
		if seen[clean] {
			continue
		}
		seen[clean] = true
		result = append(result, clean)
	}
	return result
}

// statusEntry is one parsed `git status --porcelain=v1` change: the two
// status bytes (X is the index/staged status, Y is the worktree status) and
// the reviewable path (the new path for renames/copies, the only path
// otherwise). Retaining X and Y lets the path-set builder distinguish a
// staged-only deletion from an unstaged deletion without re-parsing.
type statusEntry struct {
	X, Y byte
	Path string
}

// parseStatusPorcelainZ parses NUL-separated `git status --porcelain=v1 -z`
// output into the ordered list of changes the executor made. Each entry has
// the byte layout "XY<sp>path<NUL>" for regular changes; rename/copy entries
// (X or Y in {R,C}) append a second "<sp>oldpath<NUL>" token after the new
// path. The parser yields only the new path so the staged review set tracks
// the post-rename surface git presents to a reviewer, and preserves the X/Y
// status bytes so callers can tell staged-only deletions apart.
//
// Malformed tails (a header without its NUL-terminated path) cause the
// parser to stop where it can no longer be sure of the next path boundary;
// returning a partial path would risk staging unrelated content.
func parseStatusPorcelainZ(s string) []statusEntry {
	var entries []statusEntry
	rest := s
	for len(rest) > 0 {
		// Each entry begins with two status bytes and a separator before the
		// path. Anything shorter is a malformed tail; stop instead of
		// returning a partial path.
		if len(rest) < 4 {
			return entries
		}
		x := rest[0]
		y := rest[1]
		// Skip the "XY " prefix. git uses a single space here for
		// --porcelain=v1; the parser does not need to tolerate a tab because
		// the v1 format documents a literal SP.
		rest = rest[3:]
		idx := strings.IndexByte(rest, 0)
		if idx < 0 {
			return entries
		}
		path := rest[:idx]
		rest = rest[idx+1:]
		if isRenameOrCopyStatus(x) || isRenameOrCopyStatus(y) {
			// Staging uses only the destination; the source is already staged by Git.
			idx2 := strings.IndexByte(rest, 0)
			if idx2 < 0 {
				if path != "" {
					entries = append(entries, statusEntry{X: x, Y: y, Path: path})
				}
				return entries
			}
			rest = rest[idx2+1:]
		}
		if path != "" {
			entries = append(entries, statusEntry{X: x, Y: y, Path: path})
		}
	}
	return entries
}

func isRenameOrCopyStatus(c byte) bool {
	return c == 'R' || c == 'C'
}

// isStagedOnlyDeletion reports a deletion that is already fully staged
// (`D `). Unstaged deletions (` D`) are not matched because git add still
// needs to stage them.
func isStagedOnlyDeletion(x, y byte) bool {
	return x == 'D' && y == ' '
}

// normalizedPathSet returns the set of worktree-relative paths in `paths`,
// normalized to slash form and with empty / "." entries dropped. The
// returned map is suitable for membership tests against
// normalizeReviewablePath output.
func normalizedPathSet(paths []string) map[string]bool {
	set := make(map[string]bool, len(paths))
	for _, p := range paths {
		clean := normalizeReviewablePath(p)
		if clean == "" {
			continue
		}
		set[clean] = true
	}
	return set
}

// normalizeReviewablePath produces the canonical slash-form representation
// of a worktree-relative path used by the review-staging set. Returns ""
// when the input collapses to an empty path or to "." so callers can use
// the empty string as a sentinel for "drop this entry".
func normalizeReviewablePath(p string) string {
	clean := p
	if clean == "" {
		return ""
	}
	clean = filepath.ToSlash(filepath.Clean(clean))
	if clean == "" || clean == "." {
		return ""
	}
	return clean
}

// nonCommittedInputDestinations returns the worktree-relative destinations
// of task input files declared with commit:false. These are context-only
// inputs Galley materializes in the worktree before the executor runs;
// review-time staging must keep them out of the supervisor diff so
// reviewable evidence only reflects executor-produced changes.
func nonCommittedInputDestinations(files []task.InputFile) []string {
	var paths []string
	for _, f := range files {
		if f.Commit {
			continue
		}
		dest := f.Destination
		if dest == "" {
			continue
		}
		paths = append(paths, dest)
	}
	return paths
}
