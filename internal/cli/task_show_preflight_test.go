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
	loaded.Preflight = &taskpkg.Preflight{AcceptanceSkeleton: &taskpkg.AcceptanceSkeletonConfig{
		Enabled: true,
		Outputs: []taskpkg.AcceptanceSkeletonOutputDef{{
			ACID:                   "AC1",
			Path:                   "internal/foo/foo_test.go",
			Kind:                   "go-test",
			Purpose:                "verify AC1",
			ImplementationRequired: true,
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
		}},
	})
}

func TestTaskShowPreflightSurfacesRuntimeOutputs(t *testing.T) {
	root := t.TempDir()
	writePreflightEnabledTask(t, root)

	runDir := filepath.Join(root, "runs", "task-cli-test-1")
	seedPreflightResult(t, runDir)

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
		"preflight_output: ac=AC1 path=internal/foo/foo_test.go kind=go-test implementation_required=true",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout missing %q: %q", want, stdout)
		}
	}
	if strings.Contains(stdout, "preflight_checkpoint:") {
		t.Fatalf("task show surfaced deprecated checkpoint evidence: %q", stdout)
	}
}
