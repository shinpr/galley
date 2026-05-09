package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/shinpr/galley/internal/galleyhome"
)

func TestResolveProfileFilesUsesRepoProfilesWhenNoOverride(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	repo := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	key, qualityPath, environmentPath, err := galleyhome.RepoProfilePaths(root, repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(qualityPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(qualityPath, []byte("quality"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(environmentPath, []byte("environment"), 0o600); err != nil {
		t.Fatal(err)
	}

	resolved, err := resolveProfileFiles(Options{Root: root}, repo)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.RepoKey != key || resolved.QualityProfileFile != qualityPath || resolved.EnvironmentProfileFile != environmentPath {
		t.Fatalf("resolved got %#v", resolved)
	}
}

func TestResolveProfileFilesKeepsExplicitOverride(t *testing.T) {
	t.Parallel()
	resolved, err := resolveProfileFiles(Options{
		Root:               t.TempDir(),
		QualityProfileFile: "/tmp/quality.yaml",
	}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if resolved.QualityProfileFile != "/tmp/quality.yaml" {
		t.Fatalf("quality override got %q", resolved.QualityProfileFile)
	}
}
