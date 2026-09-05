package proc

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestVCSCommandDeadlineKeepsFailureEvidence(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		name := "deadline"
		if canceled {
			name = "cancellation"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			body := "#!/bin/sh\necho waiting-for-remote >&2\nsleep 30\n"
			if runtime.GOOS == "windows" {
				body = "@echo off\r\necho waiting-for-remote 1>&2\r\nping -n 30 127.0.0.1 >nul\r\n"
			}
			script := writeScript(t, dir, "blocked-git", body)
			stderr := filepath.Join(dir, "git.stderr.log")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			timeout := 200 * time.Millisecond
			wantErr := ErrTimeout
			if canceled {
				timeout = 30 * time.Second
				wantErr = ErrKilled
				timer := time.AfterFunc(200*time.Millisecond, cancel)
				defer timer.Stop()
			}
			result, err := runVCSCommand(ctx, Command{WorkDir: dir, Argv: scriptArgv(script)}, RunOptions{StderrPath: stderr}, timeout)
			if !errors.Is(err, wantErr) || result.TimedOut == canceled {
				t.Fatalf("not bounded: %#v %v", result, err)
			}
			if result.Duration >= processCancelWaitLimit {
				t.Fatalf("child retained output pipes after cancellation: %s", result.Duration)
			}
			if !strings.Contains(err.Error(), "waiting-for-remote") {
				t.Fatalf("failure lost stderr: %v", err)
			}
			if data, err := os.ReadFile(stderr); err != nil || !strings.Contains(string(data), "waiting-for-remote") {
				t.Fatalf("lost capture %q: %v", data, err)
			}
			if err := os.Remove(stderr); err != nil {
				t.Fatalf("capture still open after command returned: %v", err)
			}
		})
	}
}
