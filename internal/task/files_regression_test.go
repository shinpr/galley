package task

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestYAMLFilesListsYAMLAndYmlInSortedOrder(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, name := range []string{"c.yaml", "a.yml", "b.yaml", "d.json", "e.yaml.lock", "f.yml~", "g.yaml.error.json"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("id: x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got, err := YAMLFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(dir, "a.yml"), filepath.Join(dir, "b.yaml"), filepath.Join(dir, "c.yaml")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("YAMLFiles = %q, want %q", got, want)
	}
}

func TestResolveFileSourcesFromRelativeTaskPath(t *testing.T) {
	task := Task{Files: []InputFile{{Source: "../input.md"}}}
	ResolveFileSources("draft/task.yaml", &task)
	want, err := filepath.Abs("input.md")
	if err != nil {
		t.Fatal(err)
	}
	if task.Files[0].Source != want {
		t.Fatalf("source = %q, want %q", task.Files[0].Source, want)
	}
}
