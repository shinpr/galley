package skeleton

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestSnapshotPreflightFilesExcludesGitMetadata(t *testing.T) {
	root := t.TempDir()
	if err := exec.Command("git", "init", root).Run(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src.go"), []byte("package src"), 0o600); err != nil {
		t.Fatal(err)
	}

	snap, err := snapshotPreflightFiles(context.Background(), root, "", "")
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

func TestSnapshotPreflightFilesUsesConfiguredGitBinary(t *testing.T) {
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := exec.Command(gitBin, "init", root).Run(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "source.ts"), []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", "")

	snap, err := snapshotPreflightFiles(context.Background(), root, "", gitBin)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := snap["source.ts"]; !ok {
		t.Fatal("configured Git binary must enumerate Git-visible files")
	}
}

func TestSnapshotPreflightFilesExcludesGitIgnoredCaches(t *testing.T) {
	root := t.TempDir()
	if err := exec.Command("git", "init", root).Run(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("node_modules/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "node_modules", ".vite"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "node_modules", ".vite", "results.json"), []byte("cache"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "source.ts"), []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	snap, err := snapshotPreflightFiles(context.Background(), root, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := snap["source.ts"]; !ok {
		t.Fatal("unignored source must be snapshotted")
	}
	if _, ok := snap[filepath.Join("node_modules", ".vite", "results.json")]; ok {
		t.Fatal("ignored cache must not be snapshotted")
	}
}

func TestSnapshotPreflightFilesIncludesTrackedAndNonIgnoredUntrackedFiles(t *testing.T) {
	root := t.TempDir()
	if err := exec.Command("git", "init", root).Run(); err != nil {
		t.Fatal(err)
	}
	tracked := filepath.Join(root, "tracked.txt")
	if err := os.WriteFile(tracked, []byte("tracked"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("git", "-C", root, "add", "tracked.txt").Run(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "new.txt"), []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	snap, err := snapshotPreflightFiles(context.Background(), root, "", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"tracked.txt", "new.txt"} {
		if _, ok := snap[path]; !ok {
			t.Fatalf("Git-visible file %q must be snapshotted", path)
		}
	}
}
