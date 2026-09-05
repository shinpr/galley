package queue

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStaleCorruptTaskPreservedWhileOtherWorkRecovers(t *testing.T) {
	for _, collision := range []bool{false, true} {
		t.Run(fmt.Sprint(collision), func(t *testing.T) { testStaleCorruptRecovery(t, collision) })
	}
}

func testStaleCorruptRecovery(t *testing.T, collision bool) {
	t.Helper()
	root := t.TempDir()
	if err := EnsureLayout(root); err != nil {
		t.Fatal(err)
	}
	broken := filepath.Join(root, "tasks", "running", "broken.yml")
	valid := filepath.Join(root, "tasks", "running", "valid.yaml")
	raw := "id: [broken\n"
	prior := filepath.Join(root, "tasks", "failed", "broken.yml")
	if collision {
		if err := os.WriteFile(prior, []byte("older evidence"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for path, data := range map[string]string{broken: raw, valid: "id: valid\nstatus: running\n"} {
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		old := time.Now().Add(-time.Hour)
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	if err := RecoverStaleClaims(root, time.Minute, time.Now()); err != nil {
		t.Fatal(err)
	}
	failed := filepath.Join(root, "tasks", "failed", "broken.yml")
	if collision {
		if data, err := os.ReadFile(prior); err != nil || string(data) != "older evidence" {
			t.Fatalf("older evidence changed: %q %v", data, err)
		}
		failed = filepath.Join(root, "tasks", "failed", "broken.yml.unreadable-1.yml")
	}
	if data, err := os.ReadFile(failed); err != nil || string(data) != raw {
		t.Fatalf("raw evidence lost: %q %v", data, err)
	}
	if data, err := os.ReadFile(failed + ".error.json"); err != nil || !strings.Contains(string(data), "broken.yml") {
		t.Fatalf("failure evidence lost: %q %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(root, "tasks", "queued", "valid.yaml")); err != nil {
		t.Fatal(err)
	}
	if _, err := RunningRepoCounts(root); err != nil {
		t.Fatalf("corrupt task still blocks claims: %v", err)
	}
}
