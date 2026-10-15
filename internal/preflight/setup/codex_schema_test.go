package setup

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/shinpr/galley/internal/runartifact"
	"github.com/shinpr/galley/internal/runner"
	"github.com/shinpr/galley/internal/task"
)

func TestBuildCodexSetupExecutorCommandPlanWritesSetupSchemaAndPassesEffort(t *testing.T) {
	t.Parallel()
	opts := Options{
		Task: task.Task{
			ID:       "task-codex-setup-schema",
			Executor: task.Executor{CLI: "codex", Model: "gpt-5-codex", Effort: "medium"},
		},
		WorkDir: t.TempDir(),
		RunDir:  t.TempDir(),
	}
	cmd, _, err := BuildExecutorCommandPlan(opts, []byte("{}"))
	if err != nil {
		t.Fatalf("BuildExecutorCommandPlan: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(opts.RunDir, runartifact.SetupCodexDirname, runner.CodexOutputSchemaFilename))
	if err != nil {
		t.Fatalf("read materialized setup schema: %v", err)
	}
	if !strings.Contains(string(body), "Galley Setup Executor Result") {
		t.Fatalf("setup schema derivative lost canonical title: %s", string(body))
	}
	if !slices.Contains(cmd.Argv, `model_reasoning_effort="medium"`) {
		t.Fatalf("setup codex argv missing task effort: %v", cmd.Argv)
	}
}
