package runner

// AC: AC3 — The Codex executor uses the same effective executor prompt
// content as the Claude executor for now; no provider-specific Codex
// executor prompt tuning is introduced in this task.

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
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
		Goal:   "Verify Codex prompt parity.",
		Scope: task.Scope{
			CWD:          "/tmp/codex-prompt-test",
			AllowedPaths: []string{"."},
			Permission:   "edit",
		},
		Executor: task.Executor{
			CLI:           "codex",
			Model:         "gpt-5-codex",
			Effort:        "high",
			PromptProfile: "codexized-claude-executor-v1",
			PromptMode:    "replace",
		},
	}
}

func TestCodexExecutorPromptMatchesClaudePromptForNow(t *testing.T) {
	t.Parallel()
	opts := CodexFromTask(minimalCodexTask())
	opts.Prompt = "work order body"

	effective, err := CodexEffectiveSystemPrompt(opts)
	if err != nil {
		t.Fatalf("CodexEffectiveSystemPrompt: %v", err)
	}
	want := prompts.ClaudeExecutorFull()
	if want == "" {
		t.Fatal("prompts.ClaudeExecutorFull returned empty string; embed regression?")
	}
	if effective != want {
		t.Fatalf("Codex effective system prompt differs from Claude executor prompt; got %d bytes, want %d", len(effective), len(want))
	}

	plan, err := CodexCommandPlan(opts)
	if err != nil {
		t.Fatalf("CodexCommandPlan: %v", err)
	}
	if !strings.Contains(plan.Stdin, want) {
		t.Fatal("Codex command plan stdin does not embed prompts.ClaudeExecutorFull() content")
	}
}

func TestNoSeparateCodexExecutorPromptAssetExists(t *testing.T) {
	t.Parallel()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// internal/runner/<this file> -> repo root is two levels up.
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	entries, err := os.ReadDir(filepath.Join(root, "prompts"))
	if err != nil {
		t.Fatalf("read prompts dir: %v", err)
	}
	var offenders []string
	for _, e := range entries {
		name := strings.ToLower(e.Name())
		if strings.Contains(name, "codex") && (strings.Contains(name, "executor-full") || strings.Contains(name, "executor_full")) {
			offenders = append(offenders, e.Name())
		}
	}
	if len(offenders) > 0 {
		t.Fatalf("found Codex-tuned executor-full prompt assets while AC3/D1 stands: %v", offenders)
	}
	// Cross-check: prompts package surface must not expose a Codex executor
	// accessor. We rely on the embed.go source rather than reflection because
	// package-level vars are not visible via reflect.
	src, err := os.ReadFile(filepath.Join(root, "prompts", "embed.go"))
	if err != nil {
		t.Fatalf("read prompts/embed.go: %v", err)
	}
	lowered := strings.ToLower(string(src))
	if strings.Contains(lowered, "codexexecutorfull") {
		t.Fatal("prompts package exposes a CodexExecutorFull accessor; AC3/D1 disallows codex-tuned executor prompt assets")
	}

	// Sanity check: ExecutorCLIEnum remains stable (guard against drift).
	if got := task.ExecutorCLIEnum(); !reflect.DeepEqual(got, []string{"claude", "codex"}) {
		t.Fatalf("ExecutorCLIEnum drifted: %#v", got)
	}
}
