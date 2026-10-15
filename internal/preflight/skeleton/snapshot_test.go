package skeleton

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestSnapshotPreflightFilesKeepsGitVisibleFilesOnly(t *testing.T) {
	root := t.TempDir()
	if err := exec.CommandContext(t.Context(), "git", "init", root).Run(); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"tracked.txt": "tracked",
		"new.txt":     "new",
		".gitignore":  "node_modules/\n",
		filepath.Join("node_modules", ".vite", "results.json"): "cache",
	}
	for rel, body := range files {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := exec.CommandContext(t.Context(), "git", "-C", root, "add", "tracked.txt").Run(); err != nil {
		t.Fatal(err)
	}

	snap, err := snapshotPreflightFiles(context.Background(), root, "", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"tracked.txt", "new.txt"} {
		if _, ok := snap[rel]; !ok {
			t.Fatalf("Git-visible file %q must be snapshotted", rel)
		}
	}
	for _, rel := range []string{filepath.Join("node_modules", ".vite", "results.json"), ".git", filepath.Join(".git", "index")} {
		if _, ok := snap[rel]; ok {
			t.Fatalf("ignored cache or git metadata %q must not be snapshotted", rel)
		}
	}
}

func TestSnapshotPreflightFilesUsesConfiguredGitBinary(t *testing.T) {
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := exec.CommandContext(t.Context(), gitBin, "init", root).Run(); err != nil {
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
