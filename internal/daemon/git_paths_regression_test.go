package daemon

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/shinpr/galley/internal/vcs"
	"github.com/shinpr/galley/internal/workspace"
)

func TestGitPathsRoundTrip(t *testing.T) {
	repo := initDaemonGitRepo(t)
	names := []string{" 日本語.txt", "trailing .txt ", "ordinary.txt"}
	if runtime.GOOS == "windows" {
		names = []string{" 日本語.txt", "ordinary.txt"}
	} else {
		names = append(names, "tab\tline\nquote\"slash\\.txt", "literal -> arrow.txt")
	}
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(repo, name), []byte("new"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	status, err := vcs.StatusPorcelainZ(t.Context(), vcs.Repo{WorkDir: repo})
	if err != nil {
		t.Fatal(err)
	}
	paths := reviewablePathsFromStatus(status, nil)
	if err := vcs.StagePathsForReview(t.Context(), vcs.Repo{WorkDir: repo, RunDir: t.TempDir()}, paths); err != nil {
		t.Fatal(err)
	}
	snapshot, err := workspace.CaptureSnapshot(t.Context(), repo, workspace.Options{})
	if err != nil {
		t.Fatal(err)
	}
	changed := changedFilesFromSnapshot(snapshot)
	for _, name := range names {
		if _, ok := changed[name]; !ok {
			t.Fatalf("missing path %q in %#v", name, changed)
		}
	}
	if err := vcs.AddPaths(t.Context(), vcs.Repo{WorkDir: repo, RunDir: t.TempDir()}, addEligiblePorcelainPaths(snapshot.StatusPorcelain)); err != nil {
		t.Fatal(err)
	}
}

func TestRenameWithArrowInFilenamePreservesBothEndpoints(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows filenames cannot contain >")
	}
	repo := initDaemonGitRepo(t)
	old, next := "secret -> old.txt", "new -> allowed.txt"
	if err := os.WriteFile(filepath.Join(repo, old), []byte("rename"), 0o600); err != nil {
		t.Fatal(err)
	}
	runDaemonGit(t, repo, "add", old)
	runDaemonGit(t, repo, "commit", "-m", "initial file")
	runDaemonGit(t, repo, "mv", old, next)
	snapshot, err := workspace.CaptureSnapshot(t.Context(), repo, workspace.Options{})
	if err != nil {
		t.Fatal(err)
	}
	changed := changedFilesFromSnapshot(snapshot)
	for _, path := range []string{old, next} {
		if _, ok := changed[path]; !ok {
			t.Fatalf("missing %q in %q", path, snapshot.StatusPorcelain)
		}
	}
}

func TestRenameSourceRemainsVisible(t *testing.T) {
	got := parsePorcelainPaths("R  \"secret/old\\t.txt\" -> \"src/new\\n.txt\"\n")
	want := []string{"secret/old\t.txt", "src/new\n.txt"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}
