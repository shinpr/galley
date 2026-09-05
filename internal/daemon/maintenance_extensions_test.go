package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/shinpr/galley/internal/queue"
	"github.com/shinpr/galley/internal/task"
)

func TestMaintenanceIncludesYml(t *testing.T) {
	root, repo := t.TempDir(), initDaemonGitRepo(t)
	if err := queue.EnsureLayout(root); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "tasks", "done", "task.yml")
	writeDaemonTask(t, path, repo)
	_, worktree := prepareDonePRTask(t, path, repo, "merged")
	candidates, err := tasksWithPR(root)
	if err != nil || len(candidates) != 1 || candidates[0] != path {
		t.Fatalf("missing .yml PR candidate: %q %v", candidates, err)
	}
	if err := cleanupWorktrees(t.Context(), Options{Root: root}.withDefaults()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(worktree); !os.IsNotExist(err) {
		t.Fatalf(".yml worktree not cleaned: %v", err)
	}
	loaded, err := task.Load(path)
	if err != nil || loaded.Status != "merged" {
		t.Fatalf("cleanup not recorded: %v", err)
	}
}
