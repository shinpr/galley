package daemon

import (
	"reflect"
	"testing"
)

// TestAddEligiblePorcelainPathsDropsStagedOnlyDeletion pins AC3 at the parsing
// boundary: the finalization git-add path list must exclude staged-only
// deletions (porcelain "D ") while keeping every other change kind, including
// unstaged deletions (" D"), staged adds ("A "), staged modifications ("M "),
// and renames. Re-adding an already-staged deletion would fail with a pathspec
// error; the deletion is already in the index, so it still reaches the commit.
func TestAddEligiblePorcelainPathsDropsStagedOnlyDeletion(t *testing.T) {
	porcelain := "D  src/staged-deleted.go\n" +
		" D src/unstaged-deleted.go\n" +
		"A  src/added.go\n" +
		"M  src/modified.go\n" +
		"R  src/old.go -> src/new.go\n"

	got := addEligiblePorcelainPaths(porcelain)
	want := []string{
		"src/unstaged-deleted.go",
		"src/added.go",
		"src/modified.go",
		"src/new.go",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("addEligiblePorcelainPaths = %v, want %v", got, want)
	}
}

// TestAddEligiblePorcelainPathsEmptyWhenOnlyStagedDeletions pins the
// empty-list boundary: when every reported change is a staged-only deletion,
// the add list is empty. finalizeAcceptedChange relies on this so it can skip
// `git add` (which errors on an empty pathspec list) and commit the
// already-staged deletion directly.
func TestAddEligiblePorcelainPathsEmptyWhenOnlyStagedDeletions(t *testing.T) {
	porcelain := "D  a.txt\nD  b.txt\n"
	if got := addEligiblePorcelainPaths(porcelain); len(got) != 0 {
		t.Fatalf("addEligiblePorcelainPaths = %v, want empty", got)
	}
}

// TestParsePorcelainPathsStillReportsStagedDeletion pins AC4: the
// change-visibility parser used by progress detection, the finalize-time
// forbidden_paths gate, and scope-expansion reporting must keep reporting
// staged-only deletions. The fix only changes which paths reach git add, not
// which paths are considered changed/reviewable, so forbidden-path deletions
// stay visible to the safety gate.
func TestParsePorcelainPathsStillReportsStagedDeletion(t *testing.T) {
	porcelain := "D  secret/leak.txt\nA  src/added.go\n"
	got := parsePorcelainPaths(porcelain)
	want := []string{"secret/leak.txt", "src/added.go"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parsePorcelainPaths = %v, want %v (staged deletion must remain visible)", got, want)
	}
}
