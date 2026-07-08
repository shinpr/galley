package skeleton

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSnapshotPreflightFilesExcludesGitMetadata(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "index"), []byte("gitstate"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src.go"), []byte("package src"), 0o600); err != nil {
		t.Fatal(err)
	}

	snap, err := snapshotPreflightFiles(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := snap["src.go"]; !ok {
		t.Fatal("regular source file must be snapshotted")
	}
	for rel := range snap {
		if rel == ".git" || rel == filepath.Join(".git", "index") {
			t.Fatalf("git metadata must be excluded from the snapshot, found %q", rel)
		}
	}
}
