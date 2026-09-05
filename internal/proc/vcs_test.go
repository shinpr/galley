package proc

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestVCSCommandDeadlineKeepsFailureEvidence(t *testing.T) {
	dir := t.TempDir()
	body := "#!/bin/sh\necho waiting-for-remote >&2\nsleep 30\n"
	if runtime.GOOS == "windows" {
		body = "@echo off\r\necho waiting-for-remote 1>&2\r\nping -n 30 127.0.0.1 >nul\r\n"
	}
	script := writeScript(t, dir, "blocked-git", body)
	stderr := filepath.Join(dir, "git.stderr.log")
	result, err := runVCSCommand(t.Context(), Command{WorkDir: dir, Argv: scriptArgv(script)}, RunOptions{StderrPath: stderr}, 200*time.Millisecond)
	if !errors.Is(err, ErrTimeout) || !result.TimedOut {
		t.Fatalf("not bounded: %#v %v", result, err)
	}
	if !strings.Contains(err.Error(), "waiting-for-remote") {
		t.Fatalf("failure lost stderr: %v", err)
	}
	if data, err := os.ReadFile(stderr); err != nil || !strings.Contains(string(data), "waiting-for-remote") {
		t.Fatalf("lost capture %q: %v", data, err)
	}
}
