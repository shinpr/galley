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

func TestRunCommandIdleTimeoutTerminatesStalledProcessGroup(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	marker := filepath.Join(dir, "child-finished")
	// Emit output once, then become idle while a backgrounded child keeps running.
	script := writeScript(t, dir, "idle", "#!/bin/sh\necho starting\n(sleep 3; touch "+marker+") &\nsleep 5\n")

	result, err := RunCommand(context.Background(), Command{Argv: []string{script}}, RunOptions{
		Timeout:     20 * time.Second,
		IdleTimeout: 500 * time.Millisecond,
	})
	if err == nil {
		t.Fatal("expected idle timeout error")
	}
	if !result.IdleTimedOut {
		t.Fatalf("expected idle timed out result: %#v", result)
	}
	if result.TimedOut {
		t.Fatalf("idle timeout must be reported separately from total timeout: %#v", result)
	}
	if !strings.Contains(result.Stdout, "starting") {
		t.Fatalf("expected initial output captured, got %q", result.Stdout)
	}
	if !strings.Contains(err.Error(), "idle timeout") {
		t.Fatalf("error should mention idle timeout, got %v", err)
	}
	deadline := time.Now().Add(3500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if _, statErr := os.Stat(marker); os.IsNotExist(statErr) {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		t.Fatal("child process survived idle-timeout process group kill")
	}
}

func TestRunCommandIdleTimeoutNotTriggeredByOngoingOutput(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Emits output every ~100ms for ~1s, well within the idle window each time.
	script := writeScript(t, dir, "chatty", "#!/bin/sh\ni=0\nwhile [ $i -lt 8 ]; do echo line$i; sleep 0.1; i=$((i+1)); done\n")

	result, err := RunCommand(context.Background(), Command{Argv: []string{script}}, RunOptions{
		Timeout:     20 * time.Second,
		IdleTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if result.IdleTimedOut {
		t.Fatalf("idle timeout should not fire while output continues: %#v", result)
	}
	if !strings.Contains(result.Stdout, "line7") {
		t.Fatalf("expected full output, got %q", result.Stdout)
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
