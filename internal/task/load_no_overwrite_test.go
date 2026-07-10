package task

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
)

// TestWriteFileNoOverwriteAtomicCreatesAndRefuses covers the AC3 contract on
// the no-overwrite write path that backs `task queue`, `task requeue`,
// archive, and daemon claim/requeue file moves. The primitive must (1) create
// the destination atomically and (2) refuse to overwrite an existing
// destination on every supported OS.
func TestWriteFileNoOverwriteAtomicCreatesAndRefuses(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "queued.yaml")

	if err := writeFileNoOverwriteAtomic(path, []byte("first"), 0o600); err != nil {
		t.Fatalf("first write failed: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(data) != "first" {
		t.Fatalf("contents got %q, want %q", string(data), "first")
	}

	err = writeFileNoOverwriteAtomic(path, []byte("second"), 0o600)
	if err == nil {
		t.Fatalf("expected second write to refuse overwrite")
	}
	if !strings.Contains(err.Error(), "destination already exists") {
		t.Fatalf("expected duplicate destination error, got %v", err)
	}
	// The duplicate destination must not be clobbered.
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back after refusal: %v", err)
	}
	if string(data) != "first" {
		t.Fatalf("after refusal contents got %q, want %q", string(data), "first")
	}
}

// TestWriteFileNoOverwriteAtomicPublicationIsAtomic covers the queue
// publication boundary that backs `task queue`, `task requeue`, archive, and
// daemon claim/requeue file moves. Publication must happen only after the full
// contents are written and synced, so a concurrent reader that observes the
// destination either sees the complete payload or `os.ErrNotExist`, never a
// partial file that fails with a YAML decode error. The test runs many
// publish/observe pairs across goroutines so a partial publication window is
// exercised by scheduling jitter rather than relying on a fragile timing hook.
func TestWriteFileNoOverwriteAtomicPublicationIsAtomic(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// 64 KiB payload: large enough that a non-atomic O_CREATE|O_EXCL write
	// would normally split into multiple write/syscalls and be observable
	// mid-write by a fast poller, but small enough to keep the test cheap.
	payload := bytes.Repeat([]byte("0123456789abcdef"), 4096)

	const iterations = 64
	for i := 0; i < iterations; i++ {
		path := filepath.Join(dir, fmt.Sprintf("queued-%d.yaml", i))

		var wg sync.WaitGroup
		readResult := make(chan error, 1)
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				data, err := os.ReadFile(path)
				if err == nil {
					if !bytes.Equal(data, payload) {
						readResult <- fmt.Errorf(
							"partial publication observed: got %d bytes, want %d",
							len(data), len(payload),
						)
					}
					return
				}
				if !errors.Is(err, os.ErrNotExist) {
					if isTransientPublicationReadError(err) {
						continue
					}
					readResult <- fmt.Errorf("unexpected read error: %w", err)
					return
				}
			}
		}()

		if err := writeFileNoOverwriteAtomic(path, payload, 0o600); err != nil {
			t.Fatalf("iter %d write: %v", i, err)
		}
		wg.Wait()
		select {
		case err := <-readResult:
			if err != nil {
				t.Fatalf("iter %d: %v", i, err)
			}
		default:
		}

		// After successful publication the reservation lock must be gone
		// so a follow-up duplicate-destination caller surfaces the
		// expected error rather than a stale reservation conflict.
		if _, err := os.Stat(path + ".lock"); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("iter %d: reservation lock leaked: stat err=%v", i, err)
		}
	}
}

func isTransientPublicationReadError(err error) bool {
	if runtime.GOOS != "windows" {
		return false
	}
	// During same-directory rename, Windows can briefly report sharing or lock
	// violations to a racing reader. That still preserves the publication
	// contract: the reader has not observed partial YAML bytes.
	return errors.Is(err, syscall.Errno(32)) || errors.Is(err, syscall.Errno(33))
}
