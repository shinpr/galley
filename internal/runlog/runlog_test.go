package runlog

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLatestTaskRunDir(t *testing.T) {
	root := t.TempDir()
	runsDir := filepath.Join(root, "runs")
	if err := os.MkdirAll(filepath.Join(runsDir, "task-1-100"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(runsDir, "task-1-300"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(runsDir, "task-2-900"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := LatestTaskRunDir(root, "task-1")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(runsDir, "task-1-300")
	if got != want {
		t.Fatalf("LatestTaskRunDir() = %q, want %q", got, want)
	}
}

func TestLatestTaskRunDoesNotMatchNumericPrefixOfAnotherID(t *testing.T) {
	for _, name := range []string{"task-123-extra-999", "task--123"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.MkdirAll(filepath.Join(root, "runs", name), 0o700); err != nil {
				t.Fatal(err)
			}
			got, err := LatestTaskRunDir(root, "task")
			if err != nil || got != "" {
				t.Fatalf("got unrelated run %q: %v", got, err)
			}
		})
	}
}

func TestLatestAttemptDir(t *testing.T) {
	runDir := t.TempDir()
	for _, name := range []string{"attempt-1", "attempt-11", "attempt-bad", "other"} {
		if err := os.Mkdir(filepath.Join(runDir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	got, n, err := LatestAttemptDir(runDir)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(runDir, "attempt-11")
	if got != want || n != 11 {
		t.Fatalf("LatestAttemptDir() = %q, %d, want %q, 11", got, n, want)
	}
}

func TestLatestDirsMissingRoots(t *testing.T) {
	root := t.TempDir()
	runDir, err := LatestTaskRunDir(root, "missing")
	if err != nil {
		t.Fatal(err)
	}
	if runDir != "" {
		t.Fatalf("LatestTaskRunDir() = %q, want empty", runDir)
	}

	attemptDir, n, err := LatestAttemptDir(filepath.Join(root, "missing-run"))
	if err != nil {
		t.Fatal(err)
	}
	if attemptDir != "" || n != -1 {
		t.Fatalf("LatestAttemptDir() = %q, %d, want empty, -1", attemptDir, n)
	}
}
