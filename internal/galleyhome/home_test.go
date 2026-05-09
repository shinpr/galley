package galleyhome

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepoKeyIncludesBaseAndStableHash(t *testing.T) {
	t.Parallel()
	key, err := RepoKey(filepath.Join(t.TempDir(), "my repo"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(key, "my-repo-") || len(key) <= len("my-repo-") {
		t.Fatalf("repo key got %q", key)
	}
}

func TestRepoProfilePaths(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	key, quality, environment, err := RepoProfilePaths(root, filepath.Join(t.TempDir(), "repo"))
	if err != nil {
		t.Fatal(err)
	}
	if key == "" || !strings.HasPrefix(quality, filepath.Join(root, "profiles", key)) || !strings.HasPrefix(environment, filepath.Join(root, "profiles", key)) {
		t.Fatalf("paths got key=%q quality=%q environment=%q", key, quality, environment)
	}
}

func TestRepoKeyResolvesSymlinkWhenPossible(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	repo := filepath.Join(parent, "repo")
	link := filepath.Join(parent, "repo-link")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(repo, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	repoKey, err := RepoKey(repo)
	if err != nil {
		t.Fatal(err)
	}
	linkKey, err := RepoKey(link)
	if err != nil {
		t.Fatal(err)
	}
	if repoKey != linkKey {
		t.Fatalf("symlink key mismatch: repo=%q link=%q", repoKey, linkKey)
	}
}
