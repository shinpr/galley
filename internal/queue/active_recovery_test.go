package queue

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStaleRecoveryPreservesActiveTaskAndLock(t *testing.T) {
	root := t.TempDir()
	if err := EnsureLayout(root); err != nil {
		t.Fatal(err)
	}
	active := filepath.Join(root, "tasks", "running", "active.yaml")
	orphan := filepath.Join(root, "tasks", "running", "orphan.yaml")
	now := time.Now()
	old := now.Add(-2 * time.Hour)
	for _, path := range []string{active, orphan, active + ".lock", orphan + ".lock"} {
		if err := os.WriteFile(path, []byte("id: recovery-test\nstatus: running\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	if err := RecoverStaleClaimsExcept(root, time.Hour, now, map[string]bool{active: true}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{active, active + ".lock", filepath.Join(root, "tasks", "queued", "orphan.yaml")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected retained file %s: %v", path, err)
		}
	}
	for _, path := range []string{orphan, orphan + ".lock", filepath.Join(root, "tasks", "queued", "active.yaml")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("unexpected file %s: %v", path, err)
		}
	}
}
