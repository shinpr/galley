package vcs

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// AC7/AC8: every Galley-owned git invocation in the vcs package must include
// `-c core.longpaths=true` so Windows long-path operations (worktree
// removal, staging, commit, push) do not silently fail. These tests
// substitute a POSIX shell fake git binary that captures argv to disk and
// asserts the longpaths flag is present on every invocation built through
// the shared runner.GitArgs wrapper.

func writeFakeGit(t *testing.T, dir, capturePath string) string {
	t.Helper()
	path := filepath.Join(dir, "git")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> " + capturePath + "\n" +
		// Drain stdin so callers using --pathspec-from-file=- don't block.
		"cat >/dev/null\n" +
		"exit 0\n"
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

func assertLongpathsFlagPresent(t *testing.T, label, line string) {
	t.Helper()
	if !strings.Contains(line, "-c core.longpaths=true") {
		t.Errorf("%s missing -c core.longpaths=true: %q", label, line)
	}
}

// TestVCSGitInvocationsApplyLongpathsFlag drives every Galley-owned git
// operation in the vcs package through a capturing fake git and asserts the
// shared runner.GitArgs wrapper put `-c core.longpaths=true` on every argv.
// The Windows addPathsForOS case additionally proves the longpaths prefix
// survives stdin pathspec routing (--pathspec-from-file=-).
func TestVCSGitInvocationsApplyLongpathsFlag(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell fake git binary")
	}
	cases := []struct {
		name         string
		run          func(t *testing.T, git string) error
		requireStdin bool
	}{
		{
			name: "AddPaths",
			run: func(t *testing.T, git string) error {
				return AddPaths(t.Context(), Binaries{Git: git}, t.TempDir(), t.TempDir(), []string{"a.txt"})
			},
		},
		{
			name: "StatusPorcelainZ",
			run: func(t *testing.T, git string) error {
				_, err := StatusPorcelainZ(t.Context(), Binaries{Git: git}, t.TempDir())
				return err
			},
		},
		{
			name: "StagePathsForReview",
			run: func(t *testing.T, git string) error {
				return StagePathsForReview(t.Context(), Binaries{Git: git}, t.TempDir(), t.TempDir(), []string{"changed.go"})
			},
		},
		{
			name: "Commit",
			run: func(t *testing.T, git string) error {
				return Commit(t.Context(), Binaries{Git: git}, t.TempDir(), t.TempDir(), "msg")
			},
		},
		{
			name: "PushCurrentBranch",
			run: func(t *testing.T, git string) error {
				return PushCurrentBranch(t.Context(), Binaries{Git: git}, t.TempDir(), t.TempDir())
			},
		},
		{
			name: "addPathsForOS_windows",
			run: func(t *testing.T, git string) error {
				return addPathsForOS(t.Context(), Binaries{Git: git}, t.TempDir(), t.TempDir(), []string{"a.go", "b.go"}, "windows")
			},
			requireStdin: true,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			capture := filepath.Join(t.TempDir(), "git.args")
			fakeGit := writeFakeGit(t, t.TempDir(), capture)
			if err := tc.run(t, fakeGit); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			lines := captureLines(t, capture)
			if len(lines) == 0 {
				t.Fatalf("expected fake git to be invoked for %s", tc.name)
			}
			for _, line := range lines {
				assertLongpathsFlagPresent(t, tc.name, line)
				if tc.requireStdin && !strings.Contains(line, "--pathspec-from-file=-") {
					t.Errorf("Windows routing must keep --pathspec-from-file=- alongside longpaths flag: %q", line)
				}
			}
		})
	}
}
