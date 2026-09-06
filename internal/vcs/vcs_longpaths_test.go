package vcs

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// These tests capture VCS git argv and require core.longpaths on every
// Galley-owned invocation.

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

// TestVCSGitInvocationsApplyLongpathsFlag also covers Windows stdin pathspec
// routing, where the longpaths prefix must survive.
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
				return AddPaths(t.Context(), Repo{Bins: Binaries{Git: git}, WorkDir: t.TempDir(), RunDir: t.TempDir()}, []string{"a.txt"})
			},
		},
		{
			name: "StatusPorcelainZ",
			run: func(t *testing.T, git string) error {
				_, err := StatusPorcelainZ(t.Context(), Repo{Bins: Binaries{Git: git}, WorkDir: t.TempDir()})
				return err
			},
		},
		{
			name: "StagePathsForReview",
			run: func(t *testing.T, git string) error {
				return StagePathsForReview(t.Context(), Repo{Bins: Binaries{Git: git}, WorkDir: t.TempDir(), RunDir: t.TempDir()}, []string{"changed.go"})
			},
		},
		{
			name: "Commit",
			run: func(t *testing.T, git string) error {
				return Commit(t.Context(), Repo{Bins: Binaries{Git: git}, WorkDir: t.TempDir(), RunDir: t.TempDir()}, "msg")
			},
		},
		{
			name: "PushCurrentBranch",
			run: func(t *testing.T, git string) error {
				return PushCurrentBranch(t.Context(), Repo{Bins: Binaries{Git: git}, WorkDir: t.TempDir(), RunDir: t.TempDir()})
			},
		},
		{
			name: "addPathsForOS_windows",
			run: func(t *testing.T, git string) error {
				return addPathsForOS(t.Context(), Repo{Bins: Binaries{Git: git}, WorkDir: t.TempDir(), RunDir: t.TempDir()}, []string{"a.go", "b.go"}, "windows")
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
