package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/shinpr/galley/internal/profile"
	"github.com/shinpr/galley/internal/task"
)

func TestPrepareClaimedWorkspacePreservesEditedInputs(t *testing.T) {
	root, repo := t.TempDir(), initDaemonGitRepo(t)
	source := filepath.Join(t.TempDir(), "input.md")
	if err := os.WriteFile(source, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded := task.Task{ID: "input-reuse", Scope: task.Scope{CWD: repo}, Worktree: task.Worktree{Enabled: true, Branch: "agent/input", Path: filepath.Join(t.TempDir(), "worktree")}, Files: []task.InputFile{
		{Source: source, Destination: "committed.md", Commit: true},
		{Source: source, Destination: "context.md"},
	}}
	running := filepath.Join(root, "running.yaml")
	for i, run := range []string{"input-reuse-1", "input-reuse-2"} {
		runDir := filepath.Join(root, "runs", run)
		if err := os.MkdirAll(runDir, 0o700); err != nil {
			t.Fatal(err)
		}
		prepared, err := prepareClaimedWorkspace(t.Context(), Options{Root: root}, profile.Bundle{}, running, runDir, &loaded, task.Executor{})
		if err != nil {
			t.Fatal(err)
		}
		for _, file := range loaded.Files {
			path := filepath.Join(prepared.CWD, file.Destination)
			if i == 0 {
				if err := os.WriteFile(path, []byte("executor edits"), 0o600); err != nil {
					t.Fatal(err)
				}
			} else if data, err := os.ReadFile(path); err != nil || string(data) != "executor edits" {
				t.Fatalf("lost prior work: %q %v", data, err)
			}
		}
	}
}
