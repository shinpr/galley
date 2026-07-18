package inputfiles

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/shinpr/galley/internal/task"
)

func TestPrepareRejectsExistingDestination(t *testing.T) {
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

	_, err := Prepare(workDir, []task.InputFile{{Source: source, Destination: "docs/plan.md"}})
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

func TestPrepareRecordsPlacedContentDigest(t *testing.T) {
	t.Parallel()
	workDir := t.TempDir()
	source := filepath.Join(t.TempDir(), "requirements.md")
	if err := os.WriteFile(source, []byte("requirement v1"), 0o600); err != nil {
		t.Fatal(err)
	}

	prepared, err := Prepare(workDir, []task.InputFile{{Source: source, Destination: "docs/requirements.md"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared) != 1 {
		t.Fatalf("prepared files = %d, want 1", len(prepared))
	}
	if got, want := prepared[0].ContentSHA256, "28d5141613cab6847c7cf670a86e2a9f045f9eed50fa448d718ad3cf25e67a1c"; got != want {
		t.Fatalf("content digest = %q, want %q", got, want)
	}
	if ContractDigest(prepared) == ContractDigest([]Prepared{{Destination: "docs/requirements.md", ContentSHA256: "changed"}}) {
		t.Fatal("contract digest did not change with placed content")
	}
}

func TestCleanupNonCommittedRemovesOnlyPreparedFile(t *testing.T) {
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
	if _, err := Prepare(workDir, files); err != nil {
		t.Fatal(err)
	}
	if err := CleanupNonCommitted(workDir, files); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(workDir, "docs", "plan.md")); !os.IsNotExist(err) {
		t.Fatalf("expected input file removed, err=%v", err)
	}
	if data, err := os.ReadFile(keep); err != nil || string(data) != "keep" {
		t.Fatalf("existing sibling should remain, data=%q err=%v", string(data), err)
	}
}

func TestPrepareRejectsSymlinkSource(t *testing.T) {
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

	_, err := Prepare(workDir, []task.InputFile{{Source: link, Destination: "docs/plan.md"}})
	if err == nil {
		t.Fatal("expected symlink source error")
	}
	if _, statErr := os.Stat(filepath.Join(workDir, "docs", "plan.md")); !os.IsNotExist(statErr) {
		t.Fatalf("destination should not be created, err=%v", statErr)
	}
}

func TestPrepareRejectsDestinationParentSymlinkEscape(t *testing.T) {
	t.Parallel()
	workDir := t.TempDir()
	outside := t.TempDir()
	source := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(source, []byte("plan"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(workDir, "docs")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	_, err := Prepare(workDir, []task.InputFile{{Source: source, Destination: "docs/plan.md"}})
	if err == nil {
		t.Fatal("expected destination symlink escape error")
	}
	if _, statErr := os.Stat(filepath.Join(outside, "plan.md")); !os.IsNotExist(statErr) {
		t.Fatalf("outside destination should not be written, err=%v", statErr)
	}
}

func TestCleanupNonCommittedRejectsDestinationParentSymlinkEscape(t *testing.T) {
	t.Parallel()
	workDir := t.TempDir()
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "plan.md")
	if err := os.WriteFile(outsideFile, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(workDir, "docs")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	err := CleanupNonCommitted(workDir, []task.InputFile{{Destination: "docs/plan.md", Commit: false}})
	if err == nil {
		t.Fatal("expected destination symlink escape error")
	}
	data, readErr := os.ReadFile(outsideFile)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != "outside" {
		t.Fatalf("outside file was modified: %q", data)
	}
}

func TestPrepareRejectsLargeSource(t *testing.T) {
	t.Parallel()
	workDir := t.TempDir()
	source := filepath.Join(t.TempDir(), "large.bin")
	file, err := os.Create(source)
	if err != nil {
		t.Fatal(err)
	}
	// Sparse file keeps the test fast while exercising the size check.
	if _, err := file.Seek(maxInputFileBytes, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	_, err = Prepare(workDir, []task.InputFile{{Source: source, Destination: "docs/large.bin"}})
	if err == nil {
		t.Fatal("expected large source error")
	}
}
