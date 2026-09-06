package daemonctl

import (
	"errors"
	"os"
	"syscall"
	"testing"
	"time"
)

func TestReadPIDFileReadFailures(t *testing.T) {
	sharing := &os.PathError{Op: "open", Path: "daemon.pid", Err: syscall.Errno(32)}
	locked := &os.PathError{Op: "read", Path: "daemon.pid", Err: syscall.Errno(33)}
	denied := &os.PathError{Op: "open", Path: "daemon.pid", Err: syscall.Errno(5)}
	missing := &os.PathError{Op: "open", Path: "daemon.pid", Err: os.ErrNotExist}
	for _, tc := range []struct {
		name      string
		goos      string
		failures  []error
		wantErr   error
		wantCalls int
	}{
		{"sharing recovers", "windows", []error{sharing, nil}, nil, 2},
		{"lock recovers", "windows", []error{locked, nil}, nil, 2},
		{"sharing persists", "windows", []error{sharing}, sharing, 0},
		{"lock persists", "windows", []error{locked}, locked, 0},
		{"removed during retry", "windows", []error{sharing, missing}, missing, 2},
		{"permission denied", "windows", []error{denied}, denied, 1},
		{"missing", "windows", []error{missing}, missing, 1},
		{"unix errno is not Windows sharing", "darwin", []error{sharing}, sharing, 1},
		{"linux errno is not Windows locking", "linux", []error{locked}, locked, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			calls := 0
			started := time.Now()
			meta, err := readPIDFile("daemon.pid", tc.goos, stubPIDReader(t, tc.failures, &calls))
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("read error = %v, want %v", err, tc.wantErr)
			}
			if err == nil && meta.PID != 123 {
				t.Fatalf("read lost PID: %#v", meta)
			}
			assertPIDReadAttempts(t, calls, tc.wantCalls)
			if elapsed := time.Since(started); elapsed > 2*time.Second {
				t.Fatalf("PID read was not bounded: %s", elapsed)
			}
		})
	}
}

// stubPIDReader replays failures in order, repeating the last one once the list
// is exhausted, and counts every attempt.
func stubPIDReader(t *testing.T, failures []error, calls *int) func(string) ([]byte, error) {
	t.Helper()
	return func(path string) ([]byte, error) {
		if path != "daemon.pid" {
			t.Fatalf("read wrong PID file: %s", path)
		}
		failure := failures[min(*calls, len(failures)-1)]
		*calls++
		if failure != nil {
			return nil, failure
		}
		return []byte(`{"pid":123}`), nil
	}
}

// assertPIDReadAttempts checks the retry budget. wantCalls == 0 means the
// conflict is persistent, so only "was it retried at all" can be asserted.
func assertPIDReadAttempts(t *testing.T, calls, wantCalls int) {
	t.Helper()
	if wantCalls > 0 {
		if calls != wantCalls {
			t.Fatalf("read attempts = %d, want %d", calls, wantCalls)
		}
		return
	}
	if calls < 2 {
		t.Fatal("persistent conflict was not retried")
	}
}
