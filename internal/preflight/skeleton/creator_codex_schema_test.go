package skeleton

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shinpr/galley/internal/runner"
	"github.com/shinpr/galley/internal/task"
	"github.com/shinpr/galley/schemas"
)

// AC2: the skeleton creator feeds the canonical manifest schema; the
// runner boundary materializes the normalized artifact in RunDir.
func TestBuildCodexCreatorCommandPlanMaterializesNormalizedSchema(t *testing.T) {
	t.Parallel()
	opts := Options{
		Task: task.Task{
			ID:       "task-codex-skeleton-schema",
			Executor: task.Executor{CLI: "codex", Model: "gpt-5-codex", Effort: "medium"},
		},
		WorkDir: t.TempDir(),
		RunDir:  t.TempDir(),
	}
	payload := []byte(`{"task":{},"allowed_paths":[],"profiles":{},"reference_files":[]}`)
	cmd, perr := buildBuiltinCreatorCommandPlan(opts, payload)
	if perr != nil {
		t.Fatalf("buildBuiltinCreatorCommandPlan: %v", perr)
	}
	joined := strings.Join(cmd.Argv, "\n")
	if !strings.Contains(joined, "--output-schema") {
		t.Fatalf("codex creator argv missing --output-schema: %v", cmd.Argv)
	}
	body, err := os.ReadFile(filepath.Join(opts.RunDir, runner.CodexOutputSchemaFilename))
	if err != nil {
		t.Fatalf("read materialized creator schema: %v", err)
	}
	if !strings.Contains(string(body), `"outputs"`) || !strings.Contains(string(body), `"no_skeletons"`) {
		t.Fatalf("creator schema derivative lost canonical manifest markers: %s", string(body))
	}
	expected, err := runner.CodexCompatibleOutputSchema(schemas.AcceptanceSkeletonManifest)
	if err != nil {
		t.Fatalf("compute expected normalized manifest: %v", err)
	}
	if string(body) != expected {
		t.Fatalf("creator derivative must equal normalized canonical manifest; got %s", string(body))
	}
}
