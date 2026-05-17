package task

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWriteFileNoOverwriteAtomicCreatesAndRefuses covers the AC3 contract on
// the no-overwrite write path that backs `task queue`, `task requeue`,
// archive, and daemon claim/requeue file moves. Pre-fix, the implementation
// used `os.Link` against a temp file which surfaced as a raw "not supported
// by windows" error on filesystems that did not implement hardlinks. The
// replacement primitive must (1) create the destination atomically and
// (2) refuse to overwrite an existing destination on every supported OS.
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
