package skeleton

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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

	snap, err := SnapshotWorktreeFiles(context.Background(), root, "", "")
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

	snap, err := SnapshotWorktreeFiles(context.Background(), root, "", gitBin)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := snap["source.ts"]; !ok {
		t.Fatal("configured Git binary must enumerate Git-visible files")
	}
}

func TestChangedWorktreeFilesReportsCreatedModifiedAndDeletedFilesOnly(t *testing.T) {
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		if output, err := exec.CommandContext(t.Context(), "git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
	}
	write := func(rel, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, rel), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	git("init")
	write("untouched.txt", "base")
	write("modified.txt", "base")
	write("deleted.txt", "base")
	git("add", ".")
	write("earlier-untracked.txt", "earlier")
	before, err := SnapshotWorktreeFiles(t.Context(), root, "", "")
	if err != nil {
		t.Fatal(err)
	}
	write("modified.txt", "changed")
	write("created.txt", "new")
	if err := os.Remove(filepath.Join(root, "deleted.txt")); err != nil {
		t.Fatal(err)
	}

	after, err := SnapshotWorktreeFiles(t.Context(), root, "", "")
	if err != nil {
		t.Fatal(err)
	}

	got := ChangedWorktreeFiles(before, after)
	want := []string{"created.txt", "deleted.txt", "modified.txt"}
	if !slices.Equal(got, want) {
		t.Fatalf("changed files = %v, want %v", got, want)
	}
}
