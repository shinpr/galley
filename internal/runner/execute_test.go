package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunCommandCapturesOutputAndFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	script := writeScript(t, dir, "fake", "#!/bin/sh\npwd\necho err >&2\n")
	stdoutPath := filepath.Join(dir, "stdout.log")
	stderrPath := filepath.Join(dir, "stderr.log")

	result, err := RunCommand(context.Background(), Command{
		WorkDir: dir,
		Argv:    []string{script},
	}, RunOptions{
		Timeout:    5 * time.Second,
		StdoutPath: stdoutPath,
		StderrPath: stderrPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("exit code got %d", result.ExitCode)
	}
	if strings.TrimSpace(result.Stdout) != dir {
		t.Fatalf("stdout got %q", result.Stdout)
	}
	if strings.TrimSpace(result.Stderr) != "err" {
		t.Fatalf("stderr got %q", result.Stderr)
	}
	assertFileContains(t, stdoutPath, dir)
	assertFileContains(t, stderrPath, "err")
}

func TestRunCommandReturnsExitError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	script := writeScript(t, dir, "fail", "#!/bin/sh\necho nope >&2\nexit 7\n")

	result, err := RunCommand(context.Background(), Command{Argv: []string{script}}, RunOptions{Timeout: 5 * time.Second})
	if err == nil {
		t.Fatal("expected error")
	}
	if result.ExitCode != 7 {
		t.Fatalf("exit code got %d", result.ExitCode)
	}
	if !strings.Contains(result.Stderr, "nope") {
		t.Fatalf("stderr got %q", result.Stderr)
	}
}

func TestRunCommandTimeout(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	script := writeScript(t, dir, "sleep", "#!/bin/sh\nsleep 2\n")

	result, err := RunCommand(context.Background(), Command{Argv: []string{script}}, RunOptions{Timeout: 10 * time.Millisecond})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !result.TimedOut {
		t.Fatalf("expected timed out result: %#v", result)
	}
}

func TestRunCommandKillsProcessGroup(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	marker := filepath.Join(dir, "child-finished")
	script := writeScript(t, dir, "spawn", "#!/bin/sh\n(sleep 1; touch "+marker+") &\nsleep 5\n")

	result, err := RunCommand(context.Background(), Command{Argv: []string{script}}, RunOptions{Timeout: 50 * time.Millisecond})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !result.TimedOut {
		t.Fatalf("expected timed out result: %#v", result)
	}
	time.Sleep(1200 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("child process survived process group kill")
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func TestRunCommandKeepsBoundedTail(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	script := writeScript(t, dir, "output", "#!/bin/sh\nprintf 1234567890\n")

	result, err := RunCommand(context.Background(), Command{Argv: []string{script}}, RunOptions{
		Timeout:   5 * time.Second,
		TailBytes: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Stdout != "7890" {
		t.Fatalf("stdout got %q", result.Stdout)
	}
}

func writeScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func assertFileContains(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), want) {
		t.Fatalf("%s got %q, want substring %q", path, string(data), want)
	}
}
