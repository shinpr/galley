package workspace

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/shinpr/galley/internal/task"
)

// AC7/AC8: workspace.Prepare, statusPorcelain, branch lookups, and
// workspace.Remove all run through the shared runner.GitArgs argv wrapper,
// so every captured invocation must carry `-c core.longpaths=true` even
// when the operation itself is a worktree create or remove that previously
// failed on Windows MAX_PATH boundaries.

func writeFakeGit(t *testing.T, dir, capturePath string) string {
	t.Helper()
	path := filepath.Join(dir, "git")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> " + capturePath + "\n" +
		// Exit 1 so workspace.Prepare/Remove fail fast after capture; the
		// test only needs the captured argv, not real git semantics.
		"exit 1\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake git: %v", err)
	}
	return path
}

func captureLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read capture: %v", err)
	}
	var lines []string
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func TestWorkspacePrepareGitInvocationsCarryLongpathsFlag(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell fake git binary")
	}
	binDir := t.TempDir()
	repoDir := t.TempDir()
	capture := filepath.Join(t.TempDir(), "git.args")
	fakeGit := writeFakeGit(t, binDir, capture)

	// Prepare on an empty repo with a missing worktree path; the fake git
	// will return exit 1 on the very first invocation (show-ref or
	// worktree add). The captured argv is what we assert on.
	_, _ = Prepare(context.Background(), repoDir, task.Worktree{
		Enabled: true,
		Branch:  "agent/longpaths",
		Path:    filepath.Join(t.TempDir(), "workspace"),
	}, Options{GitBin: fakeGit})
	lines := captureLines(t, capture)
	if len(lines) == 0 {
		t.Fatal("expected fake git to be invoked at least once")
	}
	for _, line := range lines {
		if !strings.Contains(line, "-c core.longpaths=true") {
			t.Errorf("workspace.Prepare git invocation missing longpaths flag: %q", line)
		}
	}
}

func TestWorkspaceRemoveGitInvocationsCarryLongpathsFlag(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell fake git binary")
	}
	binDir := t.TempDir()
	repoDir := t.TempDir()
	capture := filepath.Join(t.TempDir(), "git.args")
	fakeGit := writeFakeGit(t, binDir, capture)

	// Materialize a sibling worktree path so Remove() reaches the
	// validateCleanupPath / git worktree remove path with fakeGit.
	parent := filepath.Dir(repoDir)
	worktreeDir := filepath.Join(parent, filepath.Base(repoDir)+".worktrees", "remove-target")
	if err := os.MkdirAll(worktreeDir, 0o755); err != nil {
		t.Fatalf("mkdir worktree path: %v", err)
	}
	_, _ = Remove(context.Background(), repoDir, task.Worktree{
		Enabled: true,
		Branch:  "agent/remove-longpaths",
		Path:    worktreeDir,
	}, Options{GitBin: fakeGit})
	lines := captureLines(t, capture)
	if len(lines) == 0 {
		t.Fatal("expected fake git to be invoked at least once")
	}
	for _, line := range lines {
		if !strings.Contains(line, "-c core.longpaths=true") {
			t.Errorf("workspace.Remove git invocation missing longpaths flag: %q", line)
		}
		if !strings.Contains(line, "worktree remove") && !strings.Contains(line, "rev-parse") {
			// Either is fine, just sanity check the captured shape.
			continue
		}
	}
}
