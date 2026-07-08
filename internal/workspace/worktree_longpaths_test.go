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

func assertLongpathsFlagPresent(t *testing.T, label string, lines []string) {
	t.Helper()
	if len(lines) == 0 {
		t.Fatalf("expected fake git to be invoked at least once for %s", label)
	}
	for _, line := range lines {
		if !strings.Contains(line, "-c core.longpaths=true") {
			t.Errorf("%s git invocation missing longpaths flag: %q", label, line)
		}
	}
}

// TestWorkspaceGitInvocationsCarryLongpathsFlag drives workspace.Prepare and
// workspace.Remove through a capturing fake git and asserts every argv built
// through the shared runner.GitArgs wrapper carries `-c core.longpaths=true`,
// even for the worktree create/remove operations that previously failed on
// Windows MAX_PATH boundaries.
func TestWorkspaceGitInvocationsCarryLongpathsFlag(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell fake git binary")
	}
	cases := []struct {
		name string
		run  func(t *testing.T, git string)
	}{
		{
			name: "Prepare",
			run: func(t *testing.T, git string) {
				// Prepare on an empty repo with a missing worktree path; the
				// fake git returns exit 1 on the first invocation. The captured
				// argv is what we assert on.
				_, _ = Prepare(context.Background(), t.TempDir(), task.Worktree{
					Enabled: true,
					Branch:  "agent/longpaths",
					Path:    filepath.Join(t.TempDir(), "workspace"),
				}, Options{GitBin: git})
			},
		},
		{
			name: "Remove",
			run: func(t *testing.T, git string) {
				// Materialize a sibling worktree path so Remove() reaches the
				// validateCleanupPath / git worktree remove path with the fake git.
				repoDir := t.TempDir()
				parent := filepath.Dir(repoDir)
				worktreeDir := filepath.Join(parent, filepath.Base(repoDir)+".worktrees", "remove-target")
				if err := os.MkdirAll(worktreeDir, 0o755); err != nil {
					t.Fatalf("mkdir worktree path: %v", err)
				}
				_, _ = Remove(context.Background(), repoDir, task.Worktree{
					Enabled: true,
					Branch:  "agent/remove-longpaths",
					Path:    worktreeDir,
				}, Options{GitBin: git})
			},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			capture := filepath.Join(t.TempDir(), "git.args")
			fakeGit := writeFakeGit(t, t.TempDir(), capture)
			tc.run(t, fakeGit)
			assertLongpathsFlagPresent(t, tc.name, captureLines(t, capture))
		})
	}
}
