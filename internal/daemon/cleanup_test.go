package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/shinpr/galley/internal/queue"
	"github.com/shinpr/galley/internal/task"
)

func TestCleanupWorktreesKeepsOpenPRWorktree(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	if err := queue.EnsureLayout(root); err != nil {
		t.Fatal(err)
	}
	taskPath := filepath.Join(root, "tasks", "done", "task.yaml")
	writeDaemonTask(t, taskPath, repo)
	doneTask, worktreePath := prepareDonePRTask(t, taskPath, repo, "open")
	ghBin := writeFakeCommand(t, "gh", "echo '{\"state\":\"open\",\"merged\":false}'\n")

	if err := cleanupWorktrees(context.Background(), Options{Root: root, GHBin: ghBin}.withDefaults()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(worktreePath); err != nil {
		t.Fatalf("open PR worktree should remain: %v", err)
	}
	reloaded, err := task.Load(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.PR.Status != doneTask.PR.Status || len(reloaded.Attempts) != len(doneTask.Attempts) {
		t.Fatalf("open PR task should not be updated: %#v", reloaded.PR)
	}
}

func TestCleanupWorktreesRemovesCleanMergedPRWorktree(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	if err := queue.EnsureLayout(root); err != nil {
		t.Fatal(err)
	}
	taskPath := filepath.Join(root, "tasks", "done", "task.yaml")
	writeDaemonTask(t, taskPath, repo)
	_, worktreePath := prepareDonePRTask(t, taskPath, repo, "open")
	ghBin := writeFakeCommand(t, "gh", "echo '{\"state\":\"closed\",\"merged\":true}'\n")

	if err := cleanupWorktrees(context.Background(), Options{Root: root, GHBin: ghBin}.withDefaults()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(worktreePath); !os.IsNotExist(err) {
		t.Fatalf("merged PR worktree should be removed, err=%v", err)
	}
	reloaded, err := task.Load(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.PR.Status != "merged" {
		t.Fatalf("pr status got %q", reloaded.PR.Status)
	}
	if reloaded.Status != "merged" {
		t.Fatalf("task status got %q", reloaded.Status)
	}
	if len(reloaded.Attempts) == 0 || reloaded.Attempts[len(reloaded.Attempts)-1].SupervisorVerdict != "cleanup" {
		t.Fatalf("cleanup attempt missing: %#v", reloaded.Attempts)
	}
}

func TestCleanupWorktreesRemovesDirtyClosedPRWorktree(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	if err := queue.EnsureLayout(root); err != nil {
		t.Fatal(err)
	}
	taskPath := filepath.Join(root, "tasks", "done", "task.yaml")
	writeDaemonTask(t, taskPath, repo)
	_, worktreePath := prepareDonePRTask(t, taskPath, repo, "open")
	if err := os.WriteFile(filepath.Join(worktreePath, "dirty.txt"), []byte("dirty\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ghBin := writeFakeCommand(t, "gh", "echo '{\"state\":\"closed\",\"merged\":false}'\n")

	if err := cleanupWorktrees(context.Background(), Options{Root: root, GHBin: ghBin}.withDefaults()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(worktreePath); !os.IsNotExist(err) {
		t.Fatalf("dirty worktree should be removed, err=%v", err)
	}
	reloaded, err := task.Load(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.PR.Status != "closed" {
		t.Fatalf("pr status got %q", reloaded.PR.Status)
	}
	if reloaded.Status != "closed" {
		t.Fatalf("task status got %q", reloaded.Status)
	}
	if len(reloaded.Attempts) == 0 || reloaded.Attempts[len(reloaded.Attempts)-1].SupervisorVerdict != "cleanup" {
		t.Fatalf("cleanup attempt missing: %#v", reloaded.Attempts)
	}
}

func writeFailIfInvokedGH(t *testing.T) (ghBin, marker string) {
	t.Helper()
	marker = filepath.Join(t.TempDir(), "gh-invoked")
	ghBin = writeFakeCommand(t, "gh", "printf invoked > "+strconv.Quote(marker)+"\n"+
		"echo '{\"state\":\"closed\",\"merged\":true}'\n")
	return ghBin, marker
}

func assertGHNotInvoked(t *testing.T, marker string) {
	t.Helper()
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("gh must not be invoked for a persisted-final task, marker err=%v", err)
	}
}

func TestCleanupWorktreesSkipsAlreadyFinalMissingWorktreeWithoutGitHubAPI(t *testing.T) {
	for _, prStatus := range []string{"merged", "closed"} {
		t.Run(prStatus, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), ".agent-workflow")
			repo := initDaemonGitRepo(t)
			if err := queue.EnsureLayout(root); err != nil {
				t.Fatal(err)
			}
			taskPath := filepath.Join(root, "tasks", "done", "task.yaml")
			writeDaemonTask(t, taskPath, repo)
			_, worktreePath := prepareDonePRTask(t, taskPath, repo, prStatus)
			if err := os.RemoveAll(worktreePath); err != nil {
				t.Fatal(err)
			}
			ghBin, marker := writeFailIfInvokedGH(t)

			if err := cleanupWorktrees(context.Background(), Options{Root: root, GHBin: ghBin}.withDefaults()); err != nil {
				t.Fatal(err)
			}
			assertGHNotInvoked(t, marker)

			reloaded, err := task.Load(taskPath)
			if err != nil {
				t.Fatal(err)
			}
			if reloaded.Status != prStatus {
				t.Fatalf("task status got %q want %q", reloaded.Status, prStatus)
			}
			if reloaded.PR.Status != prStatus {
				t.Fatalf("pr status got %q want %q", reloaded.PR.Status, prStatus)
			}
			old := time.Now().Add(-time.Hour)
			if err := os.Chtimes(taskPath, old, old); err != nil {
				t.Fatal(err)
			}
			before, err := os.Stat(taskPath)
			if err != nil {
				t.Fatal(err)
			}
			if err := cleanupWorktrees(t.Context(), Options{Root: root, GHBin: ghBin}.withDefaults()); err != nil {
				t.Fatal(err)
			}
			after, err := os.Stat(taskPath)
			if err != nil || !before.ModTime().Equal(after.ModTime()) {
				t.Fatalf("unchanged cleanup rewrote task: %v", err)
			}
		})
	}
}

func TestCleanupWorktreesRemovesPersistedFinalWorktreeWithoutGitHubAPI(t *testing.T) {
	for _, prStatus := range []string{"merged", "closed"} {
		t.Run(prStatus, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), ".agent-workflow")
			repo := initDaemonGitRepo(t)
			if err := queue.EnsureLayout(root); err != nil {
				t.Fatal(err)
			}
			taskPath := filepath.Join(root, "tasks", "done", "task.yaml")
			writeDaemonTask(t, taskPath, repo)
			_, worktreePath := prepareDonePRTask(t, taskPath, repo, prStatus)
			ghBin, marker := writeFailIfInvokedGH(t)

			if err := cleanupWorktrees(context.Background(), Options{Root: root, GHBin: ghBin}.withDefaults()); err != nil {
				t.Fatal(err)
			}
			assertGHNotInvoked(t, marker)

			if _, err := os.Stat(worktreePath); !os.IsNotExist(err) {
				t.Fatalf("persisted-final worktree should be removed, err=%v", err)
			}
			reloaded, err := task.Load(taskPath)
			if err != nil {
				t.Fatal(err)
			}
			if reloaded.PR.Status != prStatus {
				t.Fatalf("pr status got %q want %q", reloaded.PR.Status, prStatus)
			}
			if reloaded.Status != prStatus {
				t.Fatalf("task status got %q want %q", reloaded.Status, prStatus)
			}
			if len(reloaded.Attempts) == 0 || reloaded.Attempts[len(reloaded.Attempts)-1].SupervisorVerdict != "cleanup" {
				t.Fatalf("cleanup attempt missing: %#v", reloaded.Attempts)
			}
		})
	}
}

func TestCleanupWorktreesErrorIncludesTaskContext(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	if err := queue.EnsureLayout(root); err != nil {
		t.Fatal(err)
	}
	taskPath := filepath.Join(root, "tasks", "done", "failing.yaml")
	writeDaemonTask(t, taskPath, repo)
	prepareDonePRTask(t, taskPath, repo, "merged")
	pointWorktreeAtSourceRepo(t, taskPath, repo)
	ghBin, _ := writeFailIfInvokedGH(t)

	err := cleanupWorktrees(context.Background(), Options{Root: root, GHBin: ghBin}.withDefaults())
	if err == nil {
		t.Fatal("expected a contextualized cleanup error")
	}
	msg := err.Error()
	if !strings.Contains(msg, taskPath) && !strings.Contains(msg, "task-daemon-test") {
		t.Fatalf("error must name the task file or id, got %q", msg)
	}
	if !strings.Contains(msg, "https://github.com/example/galley/pull/123") && !strings.Contains(msg, repo) {
		t.Fatalf("error must name the PR URL or resolved worktree path, got %q", msg)
	}
}

func TestCleanupWorktreesContinuesAfterTaskFailure(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	if err := queue.EnsureLayout(root); err != nil {
		t.Fatal(err)
	}

	failingPath := filepath.Join(root, "tasks", "done", "a-failing.yaml")
	writeDaemonTask(t, failingPath, repo)
	prepareDonePRTask(t, failingPath, repo, "merged")
	pointWorktreeAtSourceRepo(t, failingPath, repo)

	removablePath := filepath.Join(root, "tasks", "done", "b-removable.yaml")
	writeDaemonTask(t, removablePath, repo)
	_, removableWorktree := prepareDonePRTask(t, removablePath, repo, "merged")

	ghBin, marker := writeFailIfInvokedGH(t)

	err := cleanupWorktrees(context.Background(), Options{Root: root, GHBin: ghBin}.withDefaults())
	if err == nil {
		t.Fatal("expected the first contextualized failure to be returned")
	}
	if !strings.Contains(err.Error(), failingPath) && !strings.Contains(err.Error(), "a-failing.yaml") {
		t.Fatalf("returned error must name the failing task, got %q", err)
	}
	assertGHNotInvoked(t, marker)

	if _, statErr := os.Stat(removableWorktree); !os.IsNotExist(statErr) {
		t.Fatalf("later removable worktree should be cleaned, err=%v", statErr)
	}
	reloaded, err := task.Load(removablePath)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Status != "merged" {
		t.Fatalf("removable task status got %q want merged", reloaded.Status)
	}
}

func TestCleanupWorktreesLogsAdditionalTaskFailures(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	if err := queue.EnsureLayout(root); err != nil {
		t.Fatal(err)
	}

	firstFailingPath := filepath.Join(root, "tasks", "done", "a-failing.yaml")
	writeDaemonTask(t, firstFailingPath, repo)
	prepareDonePRTask(t, firstFailingPath, repo, "merged")
	pointWorktreeAtSourceRepo(t, firstFailingPath, repo)

	secondFailingPath := filepath.Join(root, "tasks", "done", "b-failing.yaml")
	writeDaemonTask(t, secondFailingPath, repo)
	prepareDonePRTask(t, secondFailingPath, repo, "merged")
	pointWorktreeAtSourceRepo(t, secondFailingPath, repo)

	ghBin, marker := writeFailIfInvokedGH(t)

	var cleanupErr error
	stderr := captureStderr(t, func() {
		cleanupErr = cleanupWorktrees(context.Background(), Options{Root: root, GHBin: ghBin}.withDefaults())
	})
	if cleanupErr == nil {
		t.Fatal("expected the first contextualized failure to be returned")
	}
	if !strings.Contains(cleanupErr.Error(), firstFailingPath) {
		t.Fatalf("returned error must keep the first failing task, got %q", cleanupErr)
	}
	if !strings.Contains(stderr, "galley: additional worktree cleanup failure:") {
		t.Fatalf("stderr must log additional cleanup failures, got %q", stderr)
	}
	if !strings.Contains(stderr, secondFailingPath) {
		t.Fatalf("stderr must name the second failing task, got %q", stderr)
	}
	assertGHNotInvoked(t, marker)
}

func pointWorktreeAtSourceRepo(t *testing.T, taskPath, repo string) {
	t.Helper()
	loaded, err := task.Load(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Worktree.Path = repo
	if err := task.Save(taskPath, loaded); err != nil {
		t.Fatal(err)
	}
}
