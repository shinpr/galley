package vcs

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// These tests compile on every OS and pass an explicit goos value into
// addPathsForOS; tests that execute POSIX fake binaries skip on Windows.

// TestAddPathsForOSWindowsRoutesPathspecsThroughStdin pins AC5 for git add:
// on Windows the staged pathspec list must be delivered through
// --pathspec-from-file=- on stdin, not on argv, so a long list of changed
// files cannot push the command line past the Windows length limit.
func TestAddPathsForOSWindowsRoutesPathspecsThroughStdin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell fake git binary; Windows behavior is verified through GOOS=windows go test -exec=true compile evidence")
	}
	binDir := t.TempDir()
	runDir := t.TempDir()
	workDir := t.TempDir()
	capturePath := filepath.Join(t.TempDir(), "git.args")
	stdinPath := filepath.Join(t.TempDir(), "git.stdin")
	fakeGit := filepath.Join(binDir, "git")
	if err := os.WriteFile(fakeGit, []byte(`#!/bin/sh
printf '%s\n' "$*" > `+capturePath+`
cat > `+stdinPath+`
`), 0o700); err != nil {
		t.Fatal(err)
	}

	paths := make([]string, 0, 3000)
	for i := 0; i < 3000; i++ {
		paths = append(paths, filepath.ToSlash(filepath.Join("internal", "pkg", "long_long_filename_to_blow_up_argv_on_windows_", "file_xxxxxx.go")))
	}
	// Use distinct paths so the dedupe-aware addPathsForOS keeps a long list.
	for i := range paths {
		paths[i] = paths[i] + filepath.ToSlash("/idx_"+fmt.Sprintf("%06d", i))
	}

	if err := addPathsForOS(t.Context(), Binaries{Git: fakeGit}, workDir, runDir, paths, "windows"); err != nil {
		t.Fatal(err)
	}

	args, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatal(err)
	}
	argsStr := string(args)
	if !strings.Contains(argsStr, "--pathspec-from-file=-") {
		t.Fatalf("Windows git add must use --pathspec-from-file=-: %s", argsStr)
	}
	if !strings.Contains(argsStr, "--pathspec-file-nul") {
		t.Fatalf("Windows git add must use --pathspec-file-nul: %s", argsStr)
	}
	// Pathspecs must not be present on argv.
	if strings.Contains(argsStr, "long_long_filename_to_blow_up_argv_on_windows_") {
		t.Fatalf("Windows git add argv must not contain pathspecs, got: %s", argsStr[:200])
	}

	stdin, err := os.ReadFile(stdinPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(stdin), "long_long_filename_to_blow_up_argv_on_windows_") {
		t.Fatal("Windows git add stdin must carry the pathspec list")
	}
	if !strings.Contains(string(stdin), "\x00") {
		t.Fatal("Windows git add stdin must separate pathspecs with NUL bytes")
	}
	if !strings.HasSuffix(string(stdin), "\x00") {
		t.Fatal("Windows git add stdin must terminate the final NUL-separated pathspec")
	}
}

// TestAddPathsForOSNonWindowsKeepsArgvPathspecs pins AC4 for git add: the
// historical argv shape is preserved on macOS and Linux so existing field
// behavior continues to apply.
func TestAddPathsForOSNonWindowsKeepsArgvPathspecs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell fake git binary")
	}
	binDir := t.TempDir()
	runDir := t.TempDir()
	workDir := t.TempDir()
	capturePath := filepath.Join(t.TempDir(), "git.args")
	fakeGit := filepath.Join(binDir, "git")
	if err := os.WriteFile(fakeGit, []byte(`#!/bin/sh
printf '%s\n' "$*" > `+capturePath+`
`), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := addPathsForOS(t.Context(), Binaries{Git: fakeGit}, workDir, runDir, []string{"internal/foo.go", "internal/bar.go"}, "linux"); err != nil {
		t.Fatal(err)
	}

	args, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatal(err)
	}
	argsStr := string(args)
	if strings.Contains(argsStr, "--pathspec-from-file") {
		t.Fatalf("non-Windows git add must not introduce --pathspec-from-file: %s", argsStr)
	}
	if !strings.Contains(argsStr, "internal/foo.go") || !strings.Contains(argsStr, "internal/bar.go") {
		t.Fatalf("non-Windows git add argv must keep pathspecs on argv: %s", argsStr)
	}
}
