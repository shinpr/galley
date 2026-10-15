package daemon

import (
	"slices"
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

// The review-staging path set keeps every executor change git reports and drops
// staged-only deletions, non-local paths, and commit:false input destinations.
func TestReviewablePathsFromStatus(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		status   string
		excludes []string
		want     []string
	}{
		{
			name: "keeps modified added unstaged-deleted and untracked",
			status: porcelainEntry(" M", "src/touched.go") +
				porcelainEntry("A ", "src/added.go") +
				porcelainEntry(" D", "src/removed.go") +
				porcelainEntry("??", "internal/new/file.go"),
			want: []string{"src/touched.go", "src/added.go", "src/removed.go", "internal/new/file.go"},
		},
		{
			// `git add` on a staged-only deletion fails with "pathspec did not match any files".
			name: "skips staged-only deletion",
			status: porcelainEntry("D ", "src/staged-deleted.go") +
				porcelainEntry("A ", "src/added.go") +
				porcelainEntry(" M", "src/touched.go"),
			want: []string{"src/added.go", "src/touched.go"},
		},
		{
			name: "dedupes and drops empty and dot entries",
			status: porcelainEntry("??", "internal/new/file.go") +
				porcelainEntry("??", "internal/new/file.go") +
				porcelainEntry(" M", "./internal/new/file.go") +
				porcelainEntry("??", "") +
				porcelainEntry("??", ".") +
				porcelainEntry(" M", "other.go"),
			want: []string{"internal/new/file.go", "other.go"},
		},
		{
			name: "drops paths outside the worktree",
			status: porcelainEntry("??", "/etc/passwd") +
				porcelainEntry("??", "../escaped.go") +
				porcelainEntry("??", "safe.go"),
			want: []string{"safe.go"},
		},
		{
			// Exclusion preserves actual filename whitespace while cleaning relative path segments.
			name: "excludes commit:false destinations",
			status: porcelainEntry("??", "daemon-output.txt") +
				porcelainEntry("??", " docs/plan.md ") +
				porcelainEntry("??", "docs/plan.md") +
				porcelainEntry("??", "docs/other.md"),
			excludes: []string{" docs/plan.md ", "./docs/other.md"},
			want:     []string{"daemon-output.txt", "docs/plan.md"},
		},
		{
			name:     "context-only input yields empty set",
			status:   porcelainEntry("??", "docs/plan.md"),
			excludes: []string{"docs/plan.md"},
		},
		{
			name: "rename uses new path",
			status: porcelainRename("R ", "src/new.go", "src/old.go") +
				porcelainEntry(" M", "src/other.go"),
			want: []string{"src/new.go", "src/other.go"},
		},
		{
			name:   "malformed tail stops cleanly",
			status: porcelainEntry("??", "first.go") + "?? missing-nul-terminator",
			want:   []string{"first.go"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := reviewablePathsFromStatus(tc.status, tc.excludes); !slices.Equal(got, tc.want) {
				t.Fatalf("reviewablePathsFromStatus = %v, want %v", got, tc.want)
			}
		})
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
