package cli

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveTaskPathRecognizesBothLogicalSeparators(t *testing.T) {
	t.Parallel()
	for _, arg := range []string{"tasks/queued/example.yaml", `tasks\queued\example.yaml`} {
		got, err := resolveTaskPathOrID(t.TempDir(), arg)
		if err != nil {
			t.Fatalf("resolve %q: %v", arg, err)
		}
		if got != arg {
			t.Fatalf("resolve %q got %q", arg, got)
		}
	}
}

func TestResolveTaskPathPreservesFileLookupError(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "tasks"), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := resolveTaskPathOrID(root, "missing")
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing file error must remain inspectable: %v", err)
	}
}
