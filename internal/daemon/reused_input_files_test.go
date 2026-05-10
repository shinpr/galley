package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shinpr/galley/internal/inputfiles"
	"github.com/shinpr/galley/internal/task"
)

func TestReconcileReusedInputFilesRefreshesIdenticalContextFile(t *testing.T) {
	workDir := t.TempDir()
	srcDir := t.TempDir()
	source := filepath.Join(srcDir, "context.md")
	if err := os.WriteFile(source, []byte("same content"), 0o600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(workDir, "docs", "context.md")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("same content"), 0o600); err != nil {
		t.Fatal(err)
	}
	files := []task.InputFile{{Source: source, Destination: "docs/context.md", Commit: false}}
	if err := reconcileReusedInputFiles(workDir, files); err != nil {
		t.Fatalf("expected identical context file to be accepted, got %v", err)
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatalf("expected stale identical destination removed for clean re-copy, err=%v", err)
	}
}

func TestReconcileReusedInputFilesFailsOnConflictingContent(t *testing.T) {
	workDir := t.TempDir()
	srcDir := t.TempDir()
	source := filepath.Join(srcDir, "context.md")
	if err := os.WriteFile(source, []byte("new content"), 0o600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(workDir, "context.md")
	if err := os.WriteFile(dst, []byte("conflicting content"), 0o600); err != nil {
		t.Fatal(err)
	}
	files := []task.InputFile{{Source: source, Destination: "context.md", Commit: false}}
	err := reconcileReusedInputFiles(workDir, files)
	if err == nil {
		t.Fatal("expected conflicting destination to fail")
	}
	if !strings.Contains(err.Error(), "conflicting file") {
		t.Fatalf("error should describe the conflict clearly, got %v", err)
	}
	if data, _ := os.ReadFile(dst); string(data) != "conflicting content" {
		t.Fatalf("conflicting destination must not be modified, got %q", string(data))
	}
}

func TestReconcileReusedInputFilesIgnoresMissingDestination(t *testing.T) {
	workDir := t.TempDir()
	files := []task.InputFile{{Source: filepath.Join(t.TempDir(), "x"), Destination: "x", Commit: false}}
	if err := reconcileReusedInputFiles(workDir, files); err != nil {
		t.Fatalf("missing destination should be a no-op, got %v", err)
	}
}

func TestReconcileReusedInputFilesDoesNotEscapeViaSymlinkedDestinationParent(t *testing.T) {
	workDir := t.TempDir()
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "context.md")
	if err := os.WriteFile(outsideFile, []byte("same content"), 0o600); err != nil {
		t.Fatal(err)
	}
	srcDir := t.TempDir()
	source := filepath.Join(srcDir, "context.md")
	// Identical content: the pre-fix reconciler treated a matching destination as
	// a stale copy and removed it. The fix must refuse to read or remove through a
	// symlinked destination parent that resolves outside the worktree.
	if err := os.WriteFile(source, []byte("same content"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(workDir, "docs")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	files := []task.InputFile{{Source: source, Destination: "docs/context.md", Commit: false}}

	if err := reconcileReusedInputFiles(workDir, files); err != nil {
		t.Fatalf("symlinked destination parent must be left for inputfiles validation, got %v", err)
	}
	if data, err := os.ReadFile(outsideFile); err != nil || string(data) != "same content" {
		t.Fatalf("outside file must not be read or removed through symlinked parent, data=%q err=%v", string(data), err)
	}

	// The escape is reported by the normal inputfiles validation path instead.
	if _, err := inputfiles.Prepare(workDir, files); err == nil {
		t.Fatal("expected inputfiles.Prepare to reject the symlinked destination parent")
	}
	if data, err := os.ReadFile(outsideFile); err != nil || string(data) != "same content" {
		t.Fatalf("outside file must remain untouched after validation, data=%q err=%v", string(data), err)
	}
}

func TestReconcileReusedInputFilesLeavesCommittedFiles(t *testing.T) {
	workDir := t.TempDir()
	dst := filepath.Join(workDir, "committed.txt")
	if err := os.WriteFile(dst, []byte("anything"), 0o600); err != nil {
		t.Fatal(err)
	}
	files := []task.InputFile{{Source: filepath.Join(t.TempDir(), "committed.txt"), Destination: "committed.txt", Commit: true}}
	if err := reconcileReusedInputFiles(workDir, files); err != nil {
		t.Fatalf("committed files are left for inputfiles.Prepare, got %v", err)
	}
	if _, err := os.Stat(dst); err != nil {
		t.Fatalf("committed destination must be untouched: %v", err)
	}
}
