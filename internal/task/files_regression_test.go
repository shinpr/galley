package task

import (
	"path/filepath"
	"testing"
)

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
