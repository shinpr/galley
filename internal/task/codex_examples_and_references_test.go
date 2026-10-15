package task

import (
	"path/filepath"
	"runtime"
	"testing"
)

func repoRootFromTestFile(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// internal/task/<this_file> -> repo root is three levels up.
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func loadExampleTask(t *testing.T, rel string) Task {
	t.Helper()
	root := repoRootFromTestFile(t)
	loaded, err := Load(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("load %s: %v", rel, err)
	}
	// Match the defaults applied by public validation and queueing paths.
	ApplyDefaults(&loaded)
	return loaded
}

func TestExampleAFKTaskCodexValidates(t *testing.T) {
	t.Parallel()
	loaded := loadExampleTask(t, "examples/afk-task-codex.yaml")
	loaded.Scope.CWD = repoRootFromTestFile(t)
	result := ValidateStructural(loaded)
	if !result.Valid() {
		t.Fatalf("examples/afk-task-codex.yaml structural validation failed: %#v", result.Errors)
	}
}
