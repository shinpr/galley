package fileutil

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

	if err := WriteFileNoOverwriteAtomic(path, []byte("first"), 0o600); err != nil {
		t.Fatalf("first write failed: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(data) != "first" {
		t.Fatalf("contents got %q, want %q", string(data), "first")
	}

	err = WriteFileNoOverwriteAtomic(path, []byte("second"), 0o600)
	if err == nil {
		t.Fatalf("expected second write to refuse overwrite")
	}
	if !strings.Contains(err.Error(), "destination already exists") {
		t.Fatalf("expected duplicate destination error, got %v", err)
	}
	if !errors.Is(err, os.ErrExist) {
		t.Fatalf("duplicate error must wrap os.ErrExist: %v", err)
	}
	wantErr := fmt.Sprintf("destination already exists: %s: %s", path, os.ErrExist)
	if err.Error() != wantErr {
		t.Fatalf("duplicate error got %q, want %q", err, wantErr)
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

func TestWriteFileNoOverwriteAtomicReportsReservationAsDestinationConflict(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "queued.yaml")
	if err := os.WriteFile(path+".lock", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	err := WriteFileNoOverwriteAtomic(path, []byte("task"), 0o600)
	want := fmt.Sprintf("destination already exists: %s: %s", path, os.ErrExist)
	if err == nil || err.Error() != want || !errors.Is(err, os.ErrExist) {
		t.Fatalf("reservation conflict got %v, want %q wrapping os.ErrExist", err, want)
	}
}

func TestPublishNoReplaceConcurrentWriters(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	dst := filepath.Join(dir, "destination.yaml")
	sources := []string{filepath.Join(dir, "first.tmp"), filepath.Join(dir, "second.tmp")}
	for i, src := range sources {
		if err := os.WriteFile(src, []byte(fmt.Sprintf("writer-%d", i)), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	start := make(chan struct{})
	results := make(chan error, len(sources))
	for _, src := range sources {
		src := src
		go func() {
			<-start
			results <- publishNoReplace(src, dst)
		}()
	}
	close(start)

	var successes, conflicts int
	for range sources {
		err := <-results
		switch {
		case err == nil:
			successes++
		case errors.Is(err, os.ErrExist):
			conflicts++
		default:
			t.Fatalf("unexpected rename error: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d, want one each", successes, conflicts)
	}
	data, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "writer-0" && string(data) != "writer-1" {
		t.Fatalf("destination contains unexpected data %q", data)
	}
}

func TestRenameUnderMarkerFallbackMovesWithoutDuplicateState(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	src := filepath.Join(dir, "queued.yaml")
	dst := filepath.Join(dir, "running.yaml")
	if err := os.WriteFile(src, []byte("task"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := renameUnderMarkerFallback(src, dst); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(src); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("source still exists after state move: %v", err)
	}
	data, err := os.ReadFile(dst)
	if err != nil || string(data) != "task" {
		t.Fatalf("destination got %q, err=%v", data, err)
	}
}

func TestLinkAndUnlinkNoReplace(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	src := filepath.Join(dir, "source.yaml")
	dst := filepath.Join(dir, "destination.yaml")
	if err := os.WriteFile(src, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := linkAndUnlinkNoReplace(src, dst); !errors.Is(err, os.ErrExist) {
		t.Fatalf("existing destination error got %v, want os.ErrExist", err)
	}
	if err := os.Remove(dst); err != nil {
		t.Fatal(err)
	}
	if err := linkAndUnlinkNoReplace(src, dst); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(src); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("source still exists: %v", err)
	}
	data, err := os.ReadFile(dst)
	if err != nil || string(data) != "source" {
		t.Fatalf("destination got %q, err=%v", data, err)
	}
}

func TestWriteFileNoOverwriteAtomicSupportsLongPath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for i := 0; len(dir) < 280; i++ {
		dir = filepath.Join(dir, fmt.Sprintf("segment-%02d-abcdefghijklmnop", i))
	}
	path := filepath.Join(dir, "queued.yaml")
	if err := WriteFileNoOverwriteAtomic(path, []byte("task"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "task" {
		t.Fatalf("long-path contents got %q, err=%v", data, err)
	}
}

func TestWithExclusiveMarkerReportsCleanupFailure(t *testing.T) {
	t.Parallel()
	marker := filepath.Join(t.TempDir(), "task.lock")
	err := WithExclusiveMarker(marker, func() error {
		if err := os.Remove(marker); err != nil {
			return err
		}
		if err := os.Mkdir(marker, 0o700); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(marker, "blocker"), []byte("x"), 0o600)
	})
	if err == nil || !strings.Contains(err.Error(), "remove exclusive marker") {
		t.Fatalf("cleanup error got %v", err)
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

		if err := WriteFileNoOverwriteAtomic(path, payload, 0o600); err != nil {
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

func TestWriteFileAtomicReplacesContentsAndMode(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(path, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new" {
		t.Fatalf("contents got %q, want new", data)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("mode got %o, want 600", got)
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
