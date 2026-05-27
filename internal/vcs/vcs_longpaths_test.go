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

func TestVCSAddPathsAppliesLongpathsFlag(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell fake git binary")
	}
	binDir := t.TempDir()
	runDir := t.TempDir()
	workDir := t.TempDir()
	capture := filepath.Join(t.TempDir(), "git.args")
	fakeGit := writeFakeGit(t, binDir, capture)

	if err := AddPaths(t.Context(), Binaries{Git: fakeGit}, workDir, runDir, []string{"a.txt"}); err != nil {
		t.Fatalf("AddPaths: %v", err)
	}
	for _, line := range captureLines(t, capture) {
		assertLongpathsFlagPresent(t, "AddPaths", line)
	}
}

func TestVCSStatusPorcelainZAppliesLongpathsFlag(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell fake git binary")
	}
	binDir := t.TempDir()
	workDir := t.TempDir()
	capture := filepath.Join(t.TempDir(), "git.args")
	fakeGit := writeFakeGit(t, binDir, capture)

	if _, err := StatusPorcelainZ(t.Context(), Binaries{Git: fakeGit}, workDir); err != nil {
		t.Fatalf("StatusPorcelainZ: %v", err)
	}
	for _, line := range captureLines(t, capture) {
		assertLongpathsFlagPresent(t, "StatusPorcelainZ", line)
	}
}

func TestVCSStagePathsForReviewAppliesLongpathsFlag(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell fake git binary")
	}
	binDir := t.TempDir()
	runDir := t.TempDir()
	workDir := t.TempDir()
	capture := filepath.Join(t.TempDir(), "git.args")
	fakeGit := writeFakeGit(t, binDir, capture)

	if err := StagePathsForReview(t.Context(), Binaries{Git: fakeGit}, workDir, runDir, []string{"changed.go"}); err != nil {
		t.Fatalf("StagePathsForReview: %v", err)
	}
	for _, line := range captureLines(t, capture) {
		assertLongpathsFlagPresent(t, "StagePathsForReview", line)
	}
}

func TestVCSCommitAppliesLongpathsFlag(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell fake git binary")
	}
	binDir := t.TempDir()
	runDir := t.TempDir()
	workDir := t.TempDir()
	capture := filepath.Join(t.TempDir(), "git.args")
	fakeGit := writeFakeGit(t, binDir, capture)

	if err := Commit(t.Context(), Binaries{Git: fakeGit}, workDir, runDir, "msg"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	for _, line := range captureLines(t, capture) {
		assertLongpathsFlagPresent(t, "Commit", line)
	}
}

func TestVCSPushCurrentBranchAppliesLongpathsFlag(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell fake git binary")
	}
	binDir := t.TempDir()
	runDir := t.TempDir()
	workDir := t.TempDir()
	capture := filepath.Join(t.TempDir(), "git.args")
	fakeGit := writeFakeGit(t, binDir, capture)

	if err := PushCurrentBranch(t.Context(), Binaries{Git: fakeGit}, workDir, runDir); err != nil {
		t.Fatalf("PushCurrentBranch: %v", err)
	}
	for _, line := range captureLines(t, capture) {
		assertLongpathsFlagPresent(t, "PushCurrentBranch", line)
	}
}

// TestVCSAddPathsForOSWindowsKeepsLongpathsFlagOnStdinRouting pins the
// interaction between the Windows stdin pathspec routing and the longpaths
// argv prefix: even when the pathspec list moves off argv onto stdin via
// --pathspec-from-file=-, the longpaths flag must still appear on argv.
func TestVCSAddPathsForOSWindowsKeepsLongpathsFlagOnStdinRouting(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell fake git binary")
	}
	binDir := t.TempDir()
	runDir := t.TempDir()
	workDir := t.TempDir()
	capture := filepath.Join(t.TempDir(), "git.args")
	fakeGit := writeFakeGit(t, binDir, capture)

	if err := addPathsForOS(t.Context(), Binaries{Git: fakeGit}, workDir, runDir, []string{"a.go", "b.go"}, "windows"); err != nil {
		t.Fatalf("addPathsForOS: %v", err)
	}
	lines := captureLines(t, capture)
	if len(lines) == 0 {
		t.Fatal("expected fake git to be invoked once")
	}
	for _, line := range lines {
		assertLongpathsFlagPresent(t, "addPathsForOS(windows)", line)
		if !strings.Contains(line, "--pathspec-from-file=-") {
			t.Errorf("Windows routing must keep --pathspec-from-file=- alongside longpaths flag: %q", line)
		}
	}
}
