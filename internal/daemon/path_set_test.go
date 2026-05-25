package daemon

import (
	"reflect"
	"strings"
	"testing"
)

// porcelainEntry helps build NUL-separated `git status --porcelain=v1 -z`
// fixtures without sprinkling literal "\x00" through the test bodies.
func porcelainEntry(xy, path string) string {
	if len(xy) != 2 {
		panic("xy must be 2 bytes")
	}
	return xy + " " + path + "\x00"
}

// porcelainRename builds an "XY newpath\0oldpath\0" entry the way
// `git status --porcelain=v1 -z` reports renames and copies.
func porcelainRename(xy, newPath, oldPath string) string {
	if len(xy) != 2 {
		panic("xy must be 2 bytes")
	}
	return xy + " " + newPath + "\x00" + oldPath + "\x00"
}

// TestReviewablePathsFromStatusKeepsModifiedAddedDeletedAndUntracked pins the
// baseline behavior: the path-set builder treats every change kind git
// reports — modified (` M`), added in index (`A `), deleted (` D`), and
// untracked (`??`) — as part of the executor-produced submitted artifact.
func TestReviewablePathsFromStatusKeepsModifiedAddedDeletedAndUntracked(t *testing.T) {
	statusZ := porcelainEntry(" M", "src/touched.go") +
		porcelainEntry("A ", "src/added.go") +
		porcelainEntry(" D", "src/removed.go") +
		porcelainEntry("??", "internal/new/file.go")

	got := reviewablePathsFromStatus(statusZ, nil)
	want := []string{"src/touched.go", "src/added.go", "src/removed.go", "internal/new/file.go"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reviewablePathsFromStatus = %v, want %v", got, want)
	}
}

// TestReviewablePathsFromStatusDedupesAndDropsEmpty pins the dedupe and
// trimming behavior. Repeated porcelain entries for the same logical path
// (which can arise when git reports rename source/destination separately
// and they happen to collapse to the same string after normalization)
// produce exactly one staging entry. Empty and "."-only entries are
// dropped so the staging argv never contains a placeholder pathspec.
func TestReviewablePathsFromStatusDedupesAndDropsEmpty(t *testing.T) {
	statusZ := porcelainEntry("??", "internal/new/file.go") +
		porcelainEntry("??", "internal/new/file.go") + // duplicate
		porcelainEntry(" M", "./internal/new/file.go") + // normalizes to the same path
		porcelainEntry("??", "") + // empty path token
		porcelainEntry("??", ".") + // collapses to "."
		porcelainEntry(" M", "other.go")

	got := reviewablePathsFromStatus(statusZ, nil)
	want := []string{"internal/new/file.go", "other.go"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reviewablePathsFromStatus = %v, want %v", got, want)
	}
}

// TestReviewablePathsFromStatusDropsNonLocal pins the locality rule. Any
// porcelain entry whose path escapes the worktree (an absolute path or a
// path with a "../" segment that backs out of cwd) is dropped, because the
// review-staging set must not widen beyond the executor's working
// directory regardless of how git status formatted the entry.
func TestReviewablePathsFromStatusDropsNonLocal(t *testing.T) {
	statusZ := porcelainEntry("??", "/etc/passwd") +
		porcelainEntry("??", "../escaped.go") +
		porcelainEntry("??", "safe.go")

	got := reviewablePathsFromStatus(statusZ, nil)
	want := []string{"safe.go"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reviewablePathsFromStatus = %v, want %v", got, want)
	}
}

// TestReviewablePathsFromStatusExcludesCommitFalseDestinations pins the
// commit:false exclusion contract: task input file destinations declared
// with commit:false are context-only Galley material and must be kept out
// of the supervisor-submitted artifact even when they appear in the
// porcelain output (they are physically present in the worktree). The
// exclusion uses the same normalization rules as the include set so
// trailing whitespace, redundant "./" prefixes, and slash form differences
// do not let context inputs leak through.
func TestReviewablePathsFromStatusExcludesCommitFalseDestinations(t *testing.T) {
	statusZ := porcelainEntry("??", "daemon-output.txt") +
		porcelainEntry("??", "docs/plan.md") +
		porcelainEntry("??", "docs/other.md")

	got := reviewablePathsFromStatus(statusZ, []string{" docs/plan.md ", "./docs/other.md"})
	want := []string{"daemon-output.txt"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reviewablePathsFromStatus = %v, want %v", got, want)
	}
}

// TestReviewablePathsFromStatusKeepsForbiddenPathChanges pins the
// safety-gate boundary the supervisor feedback called out: review staging
// excludes context-only Galley material (commit:false inputs) but does not
// hide forbidden-path changes from the finalize-time gate. A path under
// task.scope.forbidden_paths must still appear in the staged review set
// (and therefore in the snapshot the forbidden_paths gate inspects in
// finalizeAcceptedChange) so the gate can fail the attempt instead of
// silently committing.
func TestReviewablePathsFromStatusKeepsForbiddenPathChanges(t *testing.T) {
	statusZ := porcelainEntry("??", "secret/leak.txt") +
		porcelainEntry("??", "src/legit.go")

	// "secret" is task.scope.forbidden_paths — not a commit:false
	// destination. The path-set builder does not see forbidden paths; only
	// the finalize-time gate does. So forbidden-path entries stay in the
	// reviewable set.
	got := reviewablePathsFromStatus(statusZ, nil)
	want := []string{"secret/leak.txt", "src/legit.go"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reviewablePathsFromStatus = %v, want %v", got, want)
	}
}

// TestReviewablePathsFromStatusNoChangeContextOnly pins the negative case
// the supervisor feedback called out explicitly: when the executor makes
// no repository change and the only worktree dirtiness is a commit:false
// input Galley materialized for context, the reviewable set is empty.
// Returning a nil/empty slice lets vcs.StagePathsForReview record a
// "skipped" evidence payload and short-circuit instead of staging the
// context input.
func TestReviewablePathsFromStatusNoChangeContextOnly(t *testing.T) {
	statusZ := porcelainEntry("??", "docs/plan.md")
	got := reviewablePathsFromStatus(statusZ, []string{"docs/plan.md"})
	if len(got) != 0 {
		t.Fatalf("reviewablePathsFromStatus = %v, want empty", got)
	}
}

// TestReviewablePathsFromStatusRenameUsesNewPath pins the rename-handling
// contract: a rename/copy porcelain entry carries the new path first and
// the old path second, both NUL-terminated. Only the new path enters the
// reviewable set so the staged snapshot matches what the supervisor reads
// as the executor's submitted surface.
func TestReviewablePathsFromStatusRenameUsesNewPath(t *testing.T) {
	statusZ := porcelainRename("R ", "src/new.go", "src/old.go") +
		porcelainEntry(" M", "src/other.go")
	got := reviewablePathsFromStatus(statusZ, nil)
	want := []string{"src/new.go", "src/other.go"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reviewablePathsFromStatus = %v, want %v", got, want)
	}
}

// TestReviewablePathsFromStatusEmptyInput pins the empty-status base case
// so callers always receive a nil/empty slice (never a partial panic) when
// `git status --porcelain` returns no entries.
func TestReviewablePathsFromStatusEmptyInput(t *testing.T) {
	if got := reviewablePathsFromStatus("", nil); len(got) != 0 {
		t.Fatalf("reviewablePathsFromStatus(empty) = %v, want empty", got)
	}
	if got := reviewablePathsFromStatus("", []string{"docs/plan.md"}); len(got) != 0 {
		t.Fatalf("reviewablePathsFromStatus(empty, excludes) = %v, want empty", got)
	}
}

// TestReviewablePathsFromStatusMalformedTailStopsCleanly pins the
// malformed-input behavior: when the porcelain stream is truncated mid-
// entry (no terminating NUL for the path), the parser yields everything it
// confidently parsed and stops. Returning a partial path would risk
// staging unrelated content.
func TestReviewablePathsFromStatusMalformedTailStopsCleanly(t *testing.T) {
	statusZ := porcelainEntry("??", "first.go") + "?? missing-nul-terminator"
	got := reviewablePathsFromStatus(statusZ, nil)
	want := []string{"first.go"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reviewablePathsFromStatus(malformed) = %v, want %v", got, want)
	}
}

// TestReviewablePathsFromStatusTrimsAndNormalizes pins that callers can
// pass status text with or without a final NUL — every NUL-terminated
// entry is honored, the trailing residue is not, and slash normalization
// is applied so paths the porcelain stream prints with backslashes (on
// Windows-style fixtures) still dedupe correctly against forward-slash
// excludes.
func TestReviewablePathsFromStatusTrimsAndNormalizes(t *testing.T) {
	// Mixed slash forms simulate a Windows porcelain capture replayed
	// against a forward-slash exclude list.
	statusZ := porcelainEntry("??", strings.ReplaceAll("docs/plan.md", "/", "\\")) +
		porcelainEntry("??", "docs/keep.md")
	got := reviewablePathsFromStatus(statusZ, []string{"docs/plan.md"})
	// On non-Windows runtimes filepath.Clean does not collapse "\\" into
	// "/", so the backslash entry is preserved literally and the exclusion
	// only triggers on the matching forward-slash entry. The assertion
	// matches that behavior on both OS families: docs/keep.md is always
	// retained, and the backslash-form sibling is dropped from the
	// reviewable set if and only if the active runtime treats it as the
	// same logical path.
	for _, p := range got {
		if p == "docs/plan.md" {
			t.Fatalf("commit:false exclusion was bypassed by normalization: %v", got)
		}
	}
	found := false
	for _, p := range got {
		if p == "docs/keep.md" {
			found = true
		}
	}
	if !found {
		t.Fatalf("docs/keep.md missing from reviewable set: %v", got)
	}
}
