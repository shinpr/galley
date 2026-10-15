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

// The Codex plan's last-message capture must be the file ResolveExecutorResult parses.
func TestBuildCodexSetupExecutorCommandPlanWritesSetupSchemaAndCapturesResult(t *testing.T) {
	t.Parallel()
	opts := Options{
		Task: task.Task{
			ID:       "task-codex-setup-schema",
			Executor: task.Executor{CLI: "codex", Model: "gpt-5-codex", Effort: "medium"},
		},
		WorkDir:  t.TempDir(),
		RunDir:   t.TempDir(),
		CodexBin: "/path/to/codex",
	}
	cmd, provider, err := BuildExecutorCommandPlan(opts, []byte("{}"))
	if err != nil {
		t.Fatalf("BuildExecutorCommandPlan: %v", err)
	}
	if provider != "codex" || cmd.Argv[0] != "/path/to/codex" {
		t.Fatalf("codex routing = %q %#v", provider, cmd.Argv)
	}
	if len(cmd.EnvAppend) != 0 {
		t.Fatalf("codex setup executor must not carry env append entries: %v", cmd.EnvAppend)
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

	i := slices.Index(cmd.Argv, "--output-last-message")
	if i < 0 || i+1 >= len(cmd.Argv) {
		t.Fatalf("codex argv missing --output-last-message: %v", cmd.Argv)
	}
	if want := filepath.Join(opts.RunDir, runartifact.SetupCodexDirname, runner.CodexLastMessageFilename); cmd.Argv[i+1] != want {
		t.Fatalf("last-message capture = %q, want %q", cmd.Argv[i+1], want)
	}
	result := "Some preamble text\n" + `{"status":"ready","commands":[{"run":"go mod download","source":"discovered","exit_code":0}],"successful_commands":[{"run":"go mod download","why":"fetch modules"}],"readiness_evidence":"ok"}`
	if err := os.WriteFile(cmd.Argv[i+1], []byte(result), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := ResolveExecutorResult(opts, "")
	if err != nil {
		t.Fatalf("ResolveExecutorResult: %v", err)
	}
	if res.Status != StatusReady || len(res.SuccessfulCommands) != 1 || res.SuccessfulCommands[0].Run != "go mod download" {
		t.Fatalf("codex parsed result: %+v", res)
	}
}
