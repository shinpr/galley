package workspace

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shinpr/galley/internal/task"
)

func TestPrepareCreatesGitWorktree(t *testing.T) {
	repo := initGitRepo(t)
	prepared, err := Prepare(context.Background(), repo, task.Worktree{
		Enabled: true,
		Branch:  "agent/test-worktree",
		Path:    "../worktrees/test-worktree",
	}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !prepared.WorktreeCreated {
		t.Fatal("expected created worktree")
	}
	if _, err := os.Stat(filepath.Join(prepared.CWD, ".git")); err != nil {
		t.Fatalf("worktree .git missing: %v", err)
	}
}

func TestPrepareReusesExistingWorktree(t *testing.T) {
	repo := initGitRepo(t)
	spec := task.Worktree{
		Enabled: true,
		Branch:  "agent/reuse-worktree",
		Path:    "../worktrees/reuse-worktree",
	}
	first, err := Prepare(context.Background(), repo, spec, Options{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Prepare(context.Background(), repo, spec, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !second.WorktreeReused {
		t.Fatalf("expected reused worktree: %#v", second)
	}
	if second.CWD != first.CWD {
		t.Fatalf("cwd got %q, want %q", second.CWD, first.CWD)
	}
}

func TestPrepareRecordsDirtyReusedWorktree(t *testing.T) {
	repo := initGitRepo(t)
	spec := task.Worktree{
		Enabled: true,
		Branch:  "agent/dirty-worktree",
		Path:    "../worktrees/dirty-worktree",
	}
	first, err := Prepare(context.Background(), repo, spec, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(first.CWD, "dirty.txt"), []byte("dirty\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := Prepare(context.Background(), repo, spec, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !second.WorktreeReused || !second.Dirty || second.StatusPorcelain == "" {
		t.Fatalf("expected dirty reused worktree: %#v", second)
	}
}

func TestPrepareUsesExistingBranchWhenPathMissing(t *testing.T) {
	repo := initGitRepo(t)
	runGit(t, repo, "branch", "agent/existing-branch")

	prepared, err := Prepare(context.Background(), repo, task.Worktree{
		Enabled: true,
		Branch:  "agent/existing-branch",
		Path:    "../worktrees/existing-branch",
	}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !prepared.WorktreeCreated {
		t.Fatalf("expected created worktree: %#v", prepared)
	}
}

// TestPrepareUsesStartPointForBrandNewBranch covers AC1: when the brand-new
// branch path of `git worktree add -b <branch> <path>` runs and a non-empty
// StartPoint is supplied, the resulting branch must start from that ref
// instead of the source repository's current HEAD.
func TestPrepareUsesStartPointForBrandNewBranch(t *testing.T) {
	repo := initGitRepo(t)
	// Create an "origin/main" ref at SHA_A and advance source HEAD to SHA_B.
	shaA := strings.TrimSpace(string(mustGitOutput(t, repo, "rev-parse", "HEAD")))
	runGit(t, repo, "update-ref", "refs/remotes/origin/main", shaA)
	if err := os.WriteFile(filepath.Join(repo, "second.txt"), []byte("second\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "second.txt")
	runGit(t, repo, "commit", "-m", "second")
	shaB := strings.TrimSpace(string(mustGitOutput(t, repo, "rev-parse", "HEAD")))
	if shaA == shaB {
		t.Fatal("setup failed: SHA_A and SHA_B should differ")
	}

	prepared, err := Prepare(context.Background(), repo, task.Worktree{
		Enabled: true,
		Branch:  "agent/start-point",
		Path:    "../worktrees/start-point",
	}, Options{StartPoint: "refs/remotes/origin/main"})
	if err != nil {
		t.Fatal(err)
	}
	if !prepared.WorktreeCreated {
		t.Fatalf("expected created worktree: %#v", prepared)
	}
	got := strings.TrimSpace(string(mustGitOutput(t, prepared.CWD, "rev-parse", "HEAD")))
	if got != shaA {
		t.Fatalf("worktree HEAD got %q, want %q (start-point ref); source HEAD=%q", got, shaA, shaB)
	}
}

// TestPrepareWithoutStartPointPreservesSourceHEAD covers AC5: when StartPoint
// is empty, Prepare must not append a trailing positional argument and the new
// branch must start from the source repository's current HEAD.
func TestPrepareWithoutStartPointPreservesSourceHEAD(t *testing.T) {
	repo := initGitRepo(t)
	headSHA := strings.TrimSpace(string(mustGitOutput(t, repo, "rev-parse", "HEAD")))

	prepared, err := Prepare(context.Background(), repo, task.Worktree{
		Enabled: true,
		Branch:  "agent/no-start-point",
		Path:    "../worktrees/no-start-point",
	}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !prepared.WorktreeCreated {
		t.Fatalf("expected created worktree: %#v", prepared)
	}
	got := strings.TrimSpace(string(mustGitOutput(t, prepared.CWD, "rev-parse", "HEAD")))
	if got != headSHA {
		t.Fatalf("worktree HEAD got %q, want source HEAD %q", got, headSHA)
	}
}

// TestPrepareIgnoresStartPointWhenWorktreePathExists covers AC6: when Prepare
// enters reuseExistingWorktree, StartPoint must be ignored. Re-pointing a
// materialized branch from a different ref would silently rewrite history.
func TestPrepareIgnoresStartPointWhenWorktreePathExists(t *testing.T) {
	repo := initGitRepo(t)
	spec := task.Worktree{
		Enabled: true,
		Branch:  "agent/reuse-start-point",
		Path:    "../worktrees/reuse-start-point",
	}
	first, err := Prepare(context.Background(), repo, spec, Options{})
	if err != nil {
		t.Fatal(err)
	}
	beforeHEAD := strings.TrimSpace(string(mustGitOutput(t, first.CWD, "rev-parse", "HEAD")))
	// Create a second commit on the source repo that we expose via
	// origin/main; if StartPoint were honored, the worktree HEAD would jump.
	if err := os.WriteFile(filepath.Join(repo, "advance.txt"), []byte("advance\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "advance.txt")
	runGit(t, repo, "commit", "-m", "advance")
	advanced := strings.TrimSpace(string(mustGitOutput(t, repo, "rev-parse", "HEAD")))
	runGit(t, repo, "update-ref", "refs/remotes/origin/main", advanced)

	second, err := Prepare(context.Background(), repo, spec, Options{StartPoint: "refs/remotes/origin/main"})
	if err != nil {
		t.Fatal(err)
	}
	if !second.WorktreeReused {
		t.Fatalf("expected reused worktree: %#v", second)
	}
	afterHEAD := strings.TrimSpace(string(mustGitOutput(t, second.CWD, "rev-parse", "HEAD")))
	if afterHEAD != beforeHEAD {
		t.Fatalf("reused worktree HEAD changed from %q to %q under StartPoint", beforeHEAD, afterHEAD)
	}
}

// TestPrepareIgnoresStartPointWhenBranchExistsButPathMissing covers AC6b: when
// the requested task branch already exists but the worktree path does not, the
// branch is reused (`git worktree add <path> <branch>`) and StartPoint must be
// ignored so the branch's existing history is not silently rewritten.
func TestPrepareIgnoresStartPointWhenBranchExistsButPathMissing(t *testing.T) {
	repo := initGitRepo(t)
	// Pre-create branch B at SHA_X.
	runGit(t, repo, "branch", "agent/existing-branch-start-point")
	shaX := strings.TrimSpace(string(mustGitOutput(t, repo, "rev-parse", "agent/existing-branch-start-point")))
	// Advance origin/main to SHA_Y, distinct from SHA_X.
	if err := os.WriteFile(filepath.Join(repo, "y.txt"), []byte("y\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "y.txt")
	runGit(t, repo, "commit", "-m", "y")
	shaY := strings.TrimSpace(string(mustGitOutput(t, repo, "rev-parse", "HEAD")))
	runGit(t, repo, "update-ref", "refs/remotes/origin/main", shaY)
	if shaX == shaY {
		t.Fatal("setup failed: SHA_X and SHA_Y should differ")
	}

	prepared, err := Prepare(context.Background(), repo, task.Worktree{
		Enabled: true,
		Branch:  "agent/existing-branch-start-point",
		Path:    "../worktrees/existing-branch-start-point",
	}, Options{StartPoint: "refs/remotes/origin/main"})
	if err != nil {
		t.Fatal(err)
	}
	if !prepared.WorktreeCreated {
		t.Fatalf("expected created worktree: %#v", prepared)
	}
	got := strings.TrimSpace(string(mustGitOutput(t, prepared.CWD, "rev-parse", "HEAD")))
	if got != shaX {
		t.Fatalf("worktree HEAD got %q, want existing branch tip %q (StartPoint=%q must be ignored)", got, shaX, shaY)
	}
}

func TestPrepareSkipsWhenDisabled(t *testing.T) {
	dir := t.TempDir()
	prepared, err := Prepare(context.Background(), dir, task.Worktree{}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.CWD != dir || prepared.WorktreeCreated {
		t.Fatalf("unexpected prepared workspace: %#v", prepared)
	}
}

func TestRemoveCleanWorktree(t *testing.T) {
	repo := initGitRepo(t)
	spec := task.Worktree{
		Enabled: true,
		Branch:  "agent/remove-worktree",
		Path:    "../worktrees/remove-worktree",
	}
	prepared, err := Prepare(context.Background(), repo, spec, Options{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := Remove(context.Background(), repo, spec, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Removed {
		t.Fatalf("expected removed: %#v", result)
	}
	if _, err := os.Stat(prepared.CWD); !os.IsNotExist(err) {
		t.Fatalf("expected worktree removed, err=%v", err)
	}
}

func TestRemoveDirtyWorktreeIsSkipped(t *testing.T) {
	repo := initGitRepo(t)
	spec := task.Worktree{
		Enabled: true,
		Branch:  "agent/dirty-remove-worktree",
		Path:    "../worktrees/dirty-remove-worktree",
	}
	prepared, err := Prepare(context.Background(), repo, spec, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prepared.CWD, "dirty.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := Remove(context.Background(), repo, spec, Options{})
	if !errors.Is(err, ErrDirtyWorktree) {
		t.Fatalf("expected dirty error, got %v", err)
	}
	if !result.Dirty {
		t.Fatalf("expected dirty result: %#v", result)
	}
	if _, statErr := os.Stat(prepared.CWD); statErr != nil {
		t.Fatalf("dirty worktree should remain: %v", statErr)
	}
}

func TestCaptureSnapshot(t *testing.T) {
	repo := initGitRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "changed.txt"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := CaptureSnapshot(context.Background(), repo, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Dirty || snapshot.StatusPorcelain == "" {
		t.Fatalf("expected dirty snapshot: %#v", snapshot)
	}
}

func TestCaptureSnapshotFromBaseDetectsExecutorCommit(t *testing.T) {
	repo := initGitRepo(t)
	base := strings.TrimSpace(string(mustGitOutput(t, repo, "rev-parse", "HEAD")))
	if err := os.WriteFile(filepath.Join(repo, "committed.txt"), []byte("committed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "committed.txt")
	runGit(t, repo, "commit", "-m", "executor commit")

	snapshot, err := CaptureSnapshotFromBase(context.Background(), repo, base, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Dirty {
		t.Fatalf("expected branch delta dirty snapshot: %#v", snapshot)
	}
	if snapshot.StatusPorcelain != "" {
		t.Fatalf("expected clean worktree status, got %q", snapshot.StatusPorcelain)
	}
	if !strings.Contains(snapshot.BranchDiff, "committed.txt") || !strings.Contains(snapshot.Diff, "committed.txt") {
		t.Fatalf("branch diff missing committed file: %#v", snapshot)
	}
}

func initGitRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "initial")
	return repo
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, output)
	}
}

func mustGitOutput(t *testing.T, dir string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, output)
	}
	return output
}
