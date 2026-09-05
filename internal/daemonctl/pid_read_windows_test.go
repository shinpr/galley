//go:build windows

package daemonctl

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestReadPIDFileSharingViolation(t *testing.T) {
	for _, release := range []bool{true, false} {
		name := "persistent"
		if release {
			name = "transient"
		}
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "daemon.pid")
			if err := WritePID(path, PIDFile{PID: os.Getpid()}); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestPIDReadLockHolder$")
			cmd.Env = append(os.Environ(), "GALLEY_TEST_PID_LOCK="+path)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			input, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			output, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() {
				_ = input.Close()
				if err := cmd.Wait(); err != nil {
					t.Errorf("lock holder: %v: %s", err, stderr.String())
				}
			}()
			if ready, err := bufio.NewReader(output).ReadString('\n'); err != nil || ready != "locked\n" {
				t.Fatalf("lock holder not ready: %q, %v", ready, err)
			}
			if _, err := os.ReadFile(path); !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
				t.Fatalf("fixture must block PID reads before testing retries: %v", err)
			}
			if release {
				timer := time.AfterFunc(75*time.Millisecond, func() { _ = input.Close() })
				defer timer.Stop()
			}
			started := time.Now()
			meta, err := ReadPIDFile(path)
			if release {
				if err != nil || meta.PID != os.Getpid() {
					t.Fatalf("read after sharing violation: %#v, %v", meta, err)
				}
			} else if !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
				t.Fatalf("persistent sharing violation lost: %v", err)
			}
			if elapsed := time.Since(started); elapsed > 2*time.Second {
				t.Fatalf("PID read was not bounded: %s", elapsed)
			}
		})
	}
}

func TestPIDReadLockHolder(t *testing.T) {
	path := os.Getenv("GALLEY_TEST_PID_LOCK")
	if path == "" {
		return
	}
	ptr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(ptr, windows.GENERIC_READ, 0, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := windows.CloseHandle(handle); err != nil {
			t.Error(err)
		}
	}()
	fmt.Println("locked")
	var release [1]byte
	_, _ = os.Stdin.Read(release[:])
}
