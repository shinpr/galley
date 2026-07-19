package setup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shinpr/galley/internal/runner"
	"github.com/shinpr/galley/internal/task"
)

// AC2: the setup executor feeds the canonical SetupResult schema; the
// runner boundary materializes the normalized artifact in RunDir.
func TestBuildCodexSetupExecutorCommandPlanMaterializesNormalizedSchema(t *testing.T) {
	t.Parallel()
	opts := Options{
		Task: task.Task{
			ID:       "task-codex-setup-schema",
			Executor: task.Executor{CLI: "codex", Model: "gpt-5-codex", Effort: "medium"},
		},
		WorkDir: t.TempDir(),
		RunDir:  t.TempDir(),
	}
	cmd, provider, err := BuildExecutorCommandPlan(opts, []byte("{}"))
	if err != nil {
		t.Fatalf("BuildExecutorCommandPlan: %v", err)
	}
	if provider != "codex" {
		t.Fatalf("provider = %q, want codex", provider)
	}
	joined := strings.Join(cmd.Argv, "\n")
	if !strings.Contains(joined, "--output-schema") {
		t.Fatalf("codex setup argv missing --output-schema: %v", cmd.Argv)
	}
	body, err := os.ReadFile(filepath.Join(opts.RunDir, runner.CodexOutputSchemaFilename))
	if err != nil {
		t.Fatalf("read materialized setup schema: %v", err)
	}
	if !strings.Contains(string(body), "Galley Setup Executor Result") {
		t.Fatalf("setup schema derivative lost canonical title: %s", string(body))
	}
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("setup schema derivative invalid JSON: %v", err)
	}
	required, _ := doc["required"].([]any)
	props, _ := doc["properties"].(map[string]any)
	if len(required) != len(props) {
		t.Fatalf("normalized setup schema must require every property: required=%d props=%d", len(required), len(props))
	}
	for _, bad := range []string{`"allOf"`, `"pattern"`, `"uniqueItems"`} {
		if strings.Contains(string(body), bad) {
			t.Fatalf("setup schema derivative must not contain %s: %s", bad, string(body))
		}
	}
}
