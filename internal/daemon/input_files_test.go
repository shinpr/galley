package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/shinpr/galley/internal/task"
)

func TestPrepareInputFilesRejectsExistingDestination(t *testing.T) {
	t.Parallel()
	workDir := t.TempDir()
	source := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(source, []byte("new plan"), 0o600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(workDir, "docs", "plan.md")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := prepareInputFiles(workDir, []task.InputFile{{Source: source, Destination: "docs/plan.md"}})
	if err == nil {
		t.Fatal("expected existing destination error")
	}
	data, readErr := os.ReadFile(dst)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != "existing" {
		t.Fatalf("existing destination was overwritten: %q", string(data))
	}
}

func TestCleanupNonCommittedInputFilesRemovesOnlyPreparedFile(t *testing.T) {
	t.Parallel()
	workDir := t.TempDir()
	source := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(source, []byte("plan"), 0o600); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(workDir, "docs", "keep.md")
	if err := os.MkdirAll(filepath.Dir(keep), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keep, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	files := []task.InputFile{{Source: source, Destination: "docs/plan.md", Commit: false}}
	if _, err := prepareInputFiles(workDir, files); err != nil {
		t.Fatal(err)
	}
	if err := cleanupNonCommittedInputFiles(workDir, files); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(workDir, "docs", "plan.md")); !os.IsNotExist(err) {
		t.Fatalf("expected input file removed, err=%v", err)
	}
	if data, err := os.ReadFile(keep); err != nil || string(data) != "keep" {
		t.Fatalf("existing sibling should remain, data=%q err=%v", string(data), err)
	}
}

func TestPrepareInputFilesRejectsSymlinkSource(t *testing.T) {
	t.Parallel()
	workDir := t.TempDir()
	sourceDir := t.TempDir()
	target := filepath.Join(sourceDir, "target.md")
	if err := os.WriteFile(target, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(sourceDir, "link.md")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	_, err := prepareInputFiles(workDir, []task.InputFile{{Source: link, Destination: "docs/plan.md"}})
	if err == nil {
		t.Fatal("expected symlink source error")
	}
	if _, statErr := os.Stat(filepath.Join(workDir, "docs", "plan.md")); !os.IsNotExist(statErr) {
		t.Fatalf("destination should not be created, err=%v", statErr)
	}
}
