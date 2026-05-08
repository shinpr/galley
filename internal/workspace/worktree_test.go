package workspace

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/shinpr/galley/internal/task"
)

func TestPrepareCreatesGitWorktree(t *testing.T) {
	repo := initGitRepo(t)
	prepared, err := Prepare(context.Background(), repo, task.Worktree{
		Enabled: true,
		Branch:  "agent/test-worktree",
		Path:    "../worktrees/test-worktree",
	})
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
	first, err := Prepare(context.Background(), repo, spec)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Prepare(context.Background(), repo, spec)
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
	first, err := Prepare(context.Background(), repo, spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(first.CWD, "dirty.txt"), []byte("dirty\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := Prepare(context.Background(), repo, spec)
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
	})
	if err != nil {
		t.Fatal(err)
	}
	if !prepared.WorktreeCreated {
		t.Fatalf("expected created worktree: %#v", prepared)
	}
}

func TestPrepareSkipsWhenDisabled(t *testing.T) {
	dir := t.TempDir()
	prepared, err := Prepare(context.Background(), dir, task.Worktree{})
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
	prepared, err := Prepare(context.Background(), repo, spec)
	if err != nil {
		t.Fatal(err)
	}
	result, err := Remove(context.Background(), repo, spec)
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
	prepared, err := Prepare(context.Background(), repo, spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prepared.CWD, "dirty.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := Remove(context.Background(), repo, spec)
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
	snapshot, err := CaptureSnapshot(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Dirty || snapshot.StatusPorcelain == "" {
		t.Fatalf("expected dirty snapshot: %#v", snapshot)
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
