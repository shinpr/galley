package task

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// lenientArchiveStatusIs reports whether the top-level `status` field in the
// given task YAML body parses to want, independent of YAML formatting.
func lenientArchiveStatusIs(body, want string) bool {
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(body), &doc); err != nil {
		return false
	}
	got, _ := doc["status"].(string)
	return got == want
}

// writeKnownFieldTypeMismatchTask writes a failed task whose known field
// loop_budget has the wrong type, so Load fails but the YAML stays a mapping.
func writeKnownFieldTypeMismatchTask(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(writeTaskYAML(t, `loop_budget: "3"`))
	if err != nil {
		t.Fatal(err)
	}
	body := strings.Replace(string(data), `status: "queued"`, `status: "failed"`, 1)
	path := filepath.Join(dir, "task.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestArchiveKnownFieldTypeMismatchUsesLenientStatusEdit(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	doneDir := filepath.Join(root, "tasks", "done")
	if err := os.MkdirAll(doneDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := writeKnownFieldTypeMismatchTask(t, doneDir)
	result, err := Archive(path, ArchiveOptions{Reason: "cleanup"})
	if err != nil {
		t.Fatalf("archive failed: %v", err)
	}
	if result.Mode != "lenient_status_edit" {
		t.Fatalf("mode got %q", result.Mode)
	}
	archivedPath := filepath.Join(root, "tasks", "archived", "task.yaml")
	if result.To != archivedPath {
		t.Fatalf("to got %q", result.To)
	}
	data, err := os.ReadFile(archivedPath)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	if !lenientArchiveStatusIs(body, "archived") {
		t.Fatalf("archived YAML missing status=archived: %q", body)
	}
	if !strings.Contains(body, `loop_budget: "3"`) {
		t.Fatalf("lenient archive must keep the rest of the document as authored: %q", body)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("source path should be moved, err=%v", err)
	}
}

// TestArchiveUnreadableMovesUnchangedWhenStatusEditUnsafe covers AC6 path 3 with
// a YAML that parses but whose top-level is not a mapping.
func TestArchiveUnreadableMovesUnchangedWhenStatusEditUnsafe(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	doneDir := filepath.Join(root, "tasks", "done")
	if err := os.MkdirAll(doneDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(doneDir, "broken.yaml")
	// A top-level sequence rather than a mapping cannot decode as a Task, and
	// editTopLevelStatus refuses to edit it. Archive must move the file unchanged.
	if err := os.WriteFile(path, []byte("- not-a-task\n- another\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := Archive(path, ArchiveOptions{})
	if err != nil {
		t.Fatalf("archive failed: %v", err)
	}
	if result.Mode != "move_unreadable_unchanged" {
		t.Fatalf("mode got %q", result.Mode)
	}
	archivedPath := filepath.Join(root, "tasks", "archived", "broken.yaml")
	data, err := os.ReadFile(archivedPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "- not-a-task\n- another\n" {
		t.Fatalf("archived bytes should round-trip unchanged, got %q", string(data))
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("source path should be moved, err=%v", err)
	}
}

func TestArchiveLenientStatusEditRefusesDestinationConflict(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	doneDir := filepath.Join(root, "tasks", "done")
	archivedDir := filepath.Join(root, "tasks", "archived")
	if err := os.MkdirAll(doneDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(archivedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := writeKnownFieldTypeMismatchTask(t, doneDir)
	existing := filepath.Join(archivedDir, "task.yaml")
	if err := os.WriteFile(existing, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Archive(path, ArchiveOptions{}); err == nil {
		t.Fatal("expected destination conflict error")
	}
	if data, err := os.ReadFile(existing); err != nil || string(data) != "existing" {
		t.Fatalf("existing archived file must stay unchanged, got %q err=%v", data, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("source task must stay in place after conflict: %v", err)
	}
}
