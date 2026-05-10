package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	taskpkg "github.com/shinpr/galley/internal/task"
)

func writePreflightJSON(t *testing.T, path string, v any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func writePreflightEnabledTask(t *testing.T, root string) {
	t.Helper()
	taskPath := writeCLITaskYAML(t)
	loaded, err := taskpkg.Load(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Status = "running"
	required := true
	loaded.Preflight = &taskpkg.Preflight{AcceptanceSkeleton: &taskpkg.AcceptanceSkeletonConfig{
		Enabled:  true,
		Required: &required,
		Outputs: []taskpkg.AcceptanceSkeletonOutputDef{{
			ACID:                   "AC1",
			Path:                   "internal/foo/foo_test.go",
			Kind:                   "go-test",
			Purpose:                "verify AC1",
			ImplementationRequired: true,
			CheckpointCommand:      "go test ./internal/foo/",
		}},
	}}
	dst := filepath.Join(root, "tasks", "running", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := taskpkg.Save(dst, loaded); err != nil {
		t.Fatal(err)
	}
}

func seedPreflightResult(t *testing.T, runDir string) {
	t.Helper()
	writePreflightJSON(t, filepath.Join(runDir, "preflight_result.json"), map[string]any{
		"status": "completed",
		"outputs": []map[string]any{{
			"ac_id":                   "AC1",
			"path":                    "internal/foo/foo_test.go",
			"kind":                    "go-test",
			"implementation_required": true,
			"checkpoint_command":      "go test ./internal/foo/",
		}},
	})
}

// TestTaskShowPreflightSurfacesLatestAttemptCheckpointEvidence proves
// `galley task show` folds the runtime preflight result and the *latest*
// attempt's skeleton_checkpoint_results.json into the view. attempt-1 recorded a
// passing checkpoint while attempt-2 (the latest) recorded a failing one — the
// view must show attempt-2's status, never attempt-1's stale pass.
func TestTaskShowPreflightSurfacesLatestAttemptCheckpointEvidence(t *testing.T) {
	root := t.TempDir()
	writePreflightEnabledTask(t, root)

	runDir := filepath.Join(root, "runs", "task-cli-test-1")
	seedPreflightResult(t, runDir)
	writePreflightJSON(t, filepath.Join(runDir, "attempt-1", "skeleton_checkpoint_results.json"),
		[]map[string]any{{"ac_id": "AC1", "command": "go test ./internal/foo/", "status": "passed"}})
	writePreflightJSON(t, filepath.Join(runDir, "attempt-2", "skeleton_checkpoint_results.json"),
		[]map[string]any{{"ac_id": "AC1", "command": "go test ./internal/foo/", "status": "failed"}})

	stdout, stderr, err := executeCommand("task", "show", "--root", root, "task-cli-test")
	if err != nil {
		t.Fatal(err)
	}
	if stderr != "" {
		t.Fatalf("stderr got %q", stderr)
	}
	for _, want := range []string{
		"preflight_enabled: true",
		"preflight_required: true",
		"preflight_runtime_status: completed",
		"preflight_output: ac=AC1 path=internal/foo/foo_test.go kind=go-test implementation_required=true checkpoint=go test ./internal/foo/",
		"preflight_checkpoint: ac=AC1 status=failed command=go test ./internal/foo/",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout missing %q: %q", want, stdout)
		}
	}
	if strings.Contains(stdout, "status=passed") {
		t.Fatalf("task show leaked stale attempt-1 passing checkpoint: %q", stdout)
	}
}

// TestTaskShowPreflightOmitsCheckpointWhenLatestAttemptHasNone proves the view
// is attempt-scoped: when attempt-2 (the latest) has no checkpoint file, the
// passing checkpoint from attempt-1 is not surfaced.
func TestTaskShowPreflightOmitsCheckpointWhenLatestAttemptHasNone(t *testing.T) {
	root := t.TempDir()
	writePreflightEnabledTask(t, root)

	runDir := filepath.Join(root, "runs", "task-cli-test-1")
	seedPreflightResult(t, runDir)
	writePreflightJSON(t, filepath.Join(runDir, "attempt-1", "skeleton_checkpoint_results.json"),
		[]map[string]any{{"ac_id": "AC1", "command": "go test ./internal/foo/", "status": "passed"}})
	if err := os.MkdirAll(filepath.Join(runDir, "attempt-2"), 0o755); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, err := executeCommand("task", "show", "--root", root, "task-cli-test")
	if err != nil {
		t.Fatal(err)
	}
	if stderr != "" {
		t.Fatalf("stderr got %q", stderr)
	}
	if !strings.Contains(stdout, "preflight_runtime_status: completed") {
		t.Fatalf("stdout missing runtime status: %q", stdout)
	}
	if strings.Contains(stdout, "preflight_checkpoint:") {
		t.Fatalf("task show surfaced a stale checkpoint when the latest attempt had none: %q", stdout)
	}
}
