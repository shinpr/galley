//go:build windows

package daemonctl

import (
	"errors"
	"os"
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
			ptr, err := windows.UTF16PtrFromString(path)
			if err != nil {
				t.Fatal(err)
			}
			handle, err := windows.CreateFile(ptr, windows.GENERIC_READ, 0, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
			if err != nil {
				t.Fatal(err)
			}
			var lockedInfo windows.ByHandleFileInformation
			lockedInfoErr := windows.GetFileInformationByHandle(handle, &lockedInfo)
			probe, probeErr := windows.CreateFile(ptr, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
			if probeErr == nil {
				_ = windows.CloseHandle(probe)
			}
			_, initialReadErr := os.ReadFile(path)
			t.Logf("exclusive handle=%d info=%+v infoErr=%v directOpenErr=%v initialReadErr=%v", handle, lockedInfo, lockedInfoErr, probeErr, initialReadErr)
			if release {
				closed := make(chan struct{})
				time.AfterFunc(75*time.Millisecond, func() {
					_ = windows.CloseHandle(handle)
					close(closed)
				})
				t.Cleanup(func() { <-closed })
			} else {
				t.Cleanup(func() { _ = windows.CloseHandle(handle) })
			}
			started := time.Now()
			meta, err := ReadPIDFile(path)
			if release {
				if err != nil || meta.PID != os.Getpid() {
					t.Fatalf("read after sharing violation: %#v, %v", meta, err)
				}
			} else if !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
				var after windows.ByHandleFileInformation
				afterErr := windows.GetFileInformationByHandle(handle, &after)
				t.Logf("after PID read: handle=%d info=%+v infoErr=%v duration=%s", handle, after, afterErr, time.Since(started))
				t.Fatalf("persistent sharing violation lost: %v", err)
			}
			if elapsed := time.Since(started); elapsed > 2*time.Second {
				t.Fatalf("PID read was not bounded: %s", elapsed)
			}
		})
	}
}
