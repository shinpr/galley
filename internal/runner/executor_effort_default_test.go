package runner

import (
	"strings"
	"testing"
)

func TestClaudeCommandPlanEmptyEffortEmitsNoOverride(t *testing.T) {
	t.Parallel()
	command, err := ClaudeCommandPlanForOS(ClaudeOptions{
		Model:          "opus",
		Effort:         "",
		PermissionMode: "bypassPermissions",
		Prompt:         "do the work",
	}, "linux")
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range command.Argv {
		if a == "--effort" {
			t.Fatalf("empty effort must not emit --effort; argv=%v", command.Argv)
		}
	}
}

func TestCodexCommandPlanEmptyEffortEmitsNoOverride(t *testing.T) {
	t.Parallel()
	base := minimalCodexTask()
	base.Executor.Effort = ""
	opts := CodexFromTask(base)
	opts.Bin = "/usr/local/bin/codex"
	opts.WorkDir = "/tmp/codex-empty-effort"
	opts.Prompt = "work order body"

	plan, err := CodexCommandPlan(opts)
	if err != nil {
		t.Fatalf("CodexCommandPlan: %v", err)
	}
	for _, a := range plan.Argv {
		if strings.HasPrefix(a, "model_reasoning_effort=") {
			t.Fatalf("empty effort must not emit model_reasoning_effort override; argv=%v", plan.Argv)
		}
	}
}

func TestGrokCommandPlanEmptyEffortEmitsNoOverride(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	plan, err := GrokCommandPlan(GrokOptions{
		Bin:            "grok-test",
		WorkDir:        dir,
		AttemptDir:     dir,
		Prompt:         "secret work order",
		SystemPrompt:   "role",
		JSONSchema:     `{"type":"object"}`,
		PermissionMode: "bypassPermissions",
		Sandbox:        "workspace",
		Effort:         "",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range plan.Argv {
		if a == "--reasoning-effort" {
			t.Fatalf("empty effort must not emit --reasoning-effort; argv=%v", plan.Argv)
		}
	}
}
