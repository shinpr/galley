package vcs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A reachable base is accepted, an unreachable one is rejected, and an
// unknown revision is an error rather than a false negative.
func TestIsAncestorSeparatesReachableUnreachableAndUnknown(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init", "-q")
	runGit(t, repo, "config", "user.email", "a@example.test")
	runGit(t, repo, "config", "user.name", "A")
	if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "base.txt")
	runGit(t, repo, "commit", "-q", "-m", "base")
	base := strings.TrimSpace(string(runGitOutput(t, repo, "rev-parse", "HEAD")))
	if err := os.WriteFile(filepath.Join(repo, "next.txt"), []byte("next\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "next.txt")
	runGit(t, repo, "commit", "-q", "-m", "next")
	head := strings.TrimSpace(string(runGitOutput(t, repo, "rev-parse", "HEAD")))

	ancestor, err := IsAncestor(t.Context(), Repo{WorkDir: repo}, base, head)
	if err != nil || !ancestor {
		t.Fatalf("base->head got (%v, %v), want (true, nil)", ancestor, err)
	}
	ancestor, err = IsAncestor(t.Context(), Repo{WorkDir: repo}, head, base)
	if err != nil || ancestor {
		t.Fatalf("head->base got (%v, %v), want (false, nil)", ancestor, err)
	}
	if _, err := IsAncestor(t.Context(), Repo{WorkDir: repo}, strings.Repeat("0", 40), head); err == nil {
		t.Fatal("unknown revision must return an error, not a false negative")
	}
}
