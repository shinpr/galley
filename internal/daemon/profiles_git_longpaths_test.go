package daemon

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// AC7/AC8: daemon profile helpers that invoke git directly
// (hasOriginRemote, fetchOriginRef, refExists) route through the shared
// runner.GitArgs argv wrapper so every captured invocation includes
// `-c core.longpaths=true`.

func writeFakeGit(t *testing.T, dir, capturePath string, exitCode int) string {
	t.Helper()
	path := filepath.Join(dir, "git")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> " + capturePath + "\n"
	if exitCode != 0 {
		script += "exit 1\n"
	} else {
		script += "exit 0\n"
	}
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

func TestHasOriginRemoteAppliesLongpathsFlag(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell fake git binary")
	}
	binDir := t.TempDir()
	sourceCWD := t.TempDir()
	capture := filepath.Join(t.TempDir(), "git.args")
	fakeGit := writeFakeGit(t, binDir, capture, 0)

	hasOriginRemote(context.Background(), Options{GitBin: fakeGit}, sourceCWD)
	for _, line := range captureLines(t, capture) {
		if !strings.Contains(line, "-c core.longpaths=true") {
			t.Errorf("hasOriginRemote git invocation missing longpaths flag: %q", line)
		}
	}
}

func TestRefExistsAppliesLongpathsFlag(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell fake git binary")
	}
	binDir := t.TempDir()
	sourceCWD := t.TempDir()
	capture := filepath.Join(t.TempDir(), "git.args")
	fakeGit := writeFakeGit(t, binDir, capture, 0)

	if _, err := refExists(context.Background(), Options{GitBin: fakeGit}, sourceCWD, "refs/heads/main"); err != nil {
		t.Fatalf("refExists: %v", err)
	}
	for _, line := range captureLines(t, capture) {
		if !strings.Contains(line, "-c core.longpaths=true") {
			t.Errorf("refExists git invocation missing longpaths flag: %q", line)
		}
	}
}

func TestFetchOriginRefAppliesLongpathsFlag(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell fake git binary")
	}
	binDir := t.TempDir()
	sourceCWD := t.TempDir()
	capture := filepath.Join(t.TempDir(), "git.args")
	fakeGit := writeFakeGit(t, binDir, capture, 0)

	if err := fetchOriginRef(context.Background(), Options{GitBin: fakeGit}, sourceCWD, "main"); err != nil {
		t.Fatalf("fetchOriginRef: %v", err)
	}
	for _, line := range captureLines(t, capture) {
		if !strings.Contains(line, "-c core.longpaths=true") {
			t.Errorf("fetchOriginRef git invocation missing longpaths flag: %q", line)
		}
	}
}
