package runner

// AC: AC3 — The Codex executor uses a provider-specific executor prompt. The
// prompt preserves the Claude executor contract while following the shorter
// Codex supervisor prompt shape.

import (
	"reflect"
	"strings"
	"testing"

	"github.com/shinpr/galley/internal/task"
	"github.com/shinpr/galley/prompts"
)

func minimalCodexTask() task.Task {
	return task.Task{
		ID:     "task-codex-prompt",
		Mode:   "afk",
		Status: "queued",
		Goal:   "Verify Codex prompt selection.",
		Scope: task.Scope{
			CWD:          "/tmp/codex-prompt-test",
			AllowedPaths: []string{"."},
			Permission:   "edit",
		},
		Executor: task.Executor{
			CLI:           "codex",
			Model:         "gpt-5-codex",
			Effort:        "high",
			PromptProfile: "codex-executor-v1",
			PromptMode:    "replace",
		},
	}
}

func TestCodexExecutorUsesCodexPromptByDefault(t *testing.T) {
	t.Parallel()
	opts := CodexFromTask(minimalCodexTask())
	opts.Prompt = "work order body"

	effective, err := CodexEffectiveSystemPrompt(opts)
	if err != nil {
		t.Fatalf("CodexEffectiveSystemPrompt: %v", err)
	}
	want := prompts.CodexExecutorFull()
	if want == "" {
		t.Fatal("prompts.CodexExecutorFull returned empty string; embed regression?")
	}
	if effective != want {
		t.Fatalf("Codex effective system prompt differs from Codex executor prompt; got %d bytes, want %d", len(effective), len(want))
	}
	if effective == prompts.ClaudeExecutorFull() {
		t.Fatal("Codex executor prompt unexpectedly matches the Claude executor prompt")
	}

	plan, err := CodexCommandPlan(opts)
	if err != nil {
		t.Fatalf("CodexCommandPlan: %v", err)
	}
	if !strings.Contains(plan.Stdin, want) {
		t.Fatal("Codex command plan stdin does not embed prompts.CodexExecutorFull() content")
	}
	if !strings.Contains(plan.Stdin, "# Work Order\n\nwork order body") {
		t.Fatal("Codex command plan stdin missing work order section")
	}
}

func TestExecutorPromptsContainContractSections(t *testing.T) {
	t.Parallel()
	required := []string{
		"# Role",
		"# Hard-Stop Conditions",
		"Work Discipline",
		"# Self Quality Gate",
		"Return exactly one JSON object",
		"task.files",
		"requested core mechanism",
		"Use exactly these enum values:",
	}
	for name, prompt := range map[string]string{
		"claude": prompts.ClaudeExecutorFull(),
		"codex":  prompts.CodexExecutorFull(),
	} {
		name := name
		prompt := prompt
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			for _, want := range required {
				if !strings.Contains(prompt, want) {
					t.Fatalf("%s executor prompt missing %q", name, want)
				}
			}
			if !strings.Contains(prompt, `"status": "completed_with_risks"`) {
				t.Fatalf("%s executor prompt missing completed_with_risks example", name)
			}
			if !strings.Contains(prompt, `"reversibility": "high"`) || !strings.Contains(prompt, `"needs_human_review": false`) {
				t.Fatalf("%s executor prompt missing decisions/risks field guidance", name)
			}
		})
	}
}

func TestCodexExecutorPromptContainsCodexSpecificContractSections(t *testing.T) {
	t.Parallel()
	prompt := prompts.CodexExecutorFull()
	for _, want := range []string{
		"# Source Priority",
		"# Required Execution Flow",
		"# Completion Rules",
		"# Output Contract",
		"Completion gates:",
		"Load and apply any skill",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("Codex executor prompt missing %q", want)
		}
	}

	// Sanity check: ExecutorCLIEnum remains stable (guard against drift).
	if got := task.ExecutorCLIEnum(); !reflect.DeepEqual(got, []string{"claude", "codex"}) {
		t.Fatalf("ExecutorCLIEnum drifted: %#v", got)
	}
}
