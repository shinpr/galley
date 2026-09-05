package vcs

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestStatusPorcelainZRetainsAllPaths(t *testing.T) {
	repo := t.TempDir()
	if out, err := exec.Command("git", "init", repo).CombinedOutput(); err != nil {
		t.Fatalf("%s: %v", out, err)
	}
	const count = 16000
	var paths []string
	for i := 0; i < count; i++ {
		name := fmt.Sprintf("%04d-%s", i, strings.Repeat("x", 150))
		paths = append(paths, name)
		if err := os.WriteFile(filepath.Join(repo, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	status, err := StatusPorcelainZ(t.Context(), Binaries{}, repo)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(status, "\x00"); n != count {
		t.Fatalf("got %d of %d paths", n, count)
	}
	if err := StagePathsForReview(t.Context(), Binaries{}, repo, t.TempDir(), paths); err != nil {
		t.Fatal(err)
	}
	if err := AddPaths(t.Context(), Binaries{}, repo, t.TempDir(), paths); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "diff", "--cached", "--name-only", "-z")
	cmd.Dir = repo
	staged, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(staged), "\x00"); n != count {
		t.Fatalf("staged %d of %d paths", n, count)
	}
}
