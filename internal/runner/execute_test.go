package runner

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestRunCommandCapturesOutputAndFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	body := "#!/bin/sh\npwd\necho err >&2\n"
	if runtime.GOOS == "windows" {
		body = "@echo off\r\ncd\r\necho err 1>&2\r\n"
	}
	script := writeScript(t, dir, "fake", body)
	stdoutPath := filepath.Join(dir, "stdout.log")
	stderrPath := filepath.Join(dir, "stderr.log")

	result, err := RunCommand(context.Background(), Command{
		WorkDir: dir,
		Argv:    scriptArgv(script),
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
	body := "#!/bin/sh\necho nope >&2\nexit 7\n"
	if runtime.GOOS == "windows" {
		body = "@echo off\r\necho nope 1>&2\r\nexit /b 7\r\n"
	}
	script := writeScript(t, dir, "fail", body)

	result, err := RunCommand(context.Background(), Command{Argv: scriptArgv(script)}, RunOptions{Timeout: 5 * time.Second})
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

func TestRunCommandKillsProcessGroup(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("process-group termination is Unix-specific")
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "child-finished")
	script := writeScript(t, dir, "spawn", "#!/bin/sh\n(sleep 1; touch "+marker+") &\nsleep 5\n")

	result, err := RunCommand(context.Background(), Command{Argv: scriptArgv(script)}, RunOptions{Timeout: 50 * time.Millisecond})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !result.TimedOut {
		t.Fatalf("expected timed out result: %#v", result)
	}
	deadline := time.Now().Add(1200 * time.Millisecond)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); os.IsNotExist(err) {
			return
		} else if err != nil {
			t.Fatal(err)
		}
		time.Sleep(25 * time.Millisecond)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("child process survived process group kill")
	}
}

func TestRunCommandKeepsBoundedTail(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	body := "#!/bin/sh\nprintf 1234567890\n"
	want := "7890"
	if runtime.GOOS == "windows" {
		body = "@echo off\r\necho 1234567890\r\n"
		want = "90\r\n"
	}
	script := writeScript(t, dir, "output", body)

	result, err := RunCommand(context.Background(), Command{Argv: scriptArgv(script)}, RunOptions{
		Timeout:   5 * time.Second,
		TailBytes: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Stdout != want {
		t.Fatalf("stdout got %q", result.Stdout)
	}
}

func writeScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		name += ".bat"
	}
	path := filepath.Join(dir, name)
	tmp, err := os.CreateTemp(dir, "."+name+".*.tmp")
	if err != nil {
		t.Fatal(err)
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.WriteString(body); err != nil {
		_ = tmp.Close()
		t.Fatal(err)
	}
	if err := tmp.Chmod(0o700); err != nil {
		_ = tmp.Close()
		t.Fatal(err)
	}
	if err := tmp.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		t.Fatal(err)
	}
	cleanup = false
	return path
}

func scriptArgv(script string) []string {
	if runtime.GOOS == "windows" {
		return []string{script}
	}
	return []string{"/bin/sh", script}
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
