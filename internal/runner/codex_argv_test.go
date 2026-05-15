package runner

// AC: AC2/AC3 — The Codex command plan must build an argv that the local
// `codex exec` CLI actually accepts. The upstream CLI does not expose a
// top-level `--effort` flag and has no `--max-budget-usd` flag at all, so the
// runner must avoid emitting those flags. Effort is delivered through the
// generic config override surface and max_budget_usd is recorded as a warning.

import (
	"strings"
	"testing"
)

func TestCodexArgvDoesNotEmitUnsupportedFlags(t *testing.T) {
	t.Parallel()
	base := minimalCodexTask()
	budget := 4.0
	base.Executor.MaxBudgetUSD = &budget
	opts := CodexFromTask(base)
	opts.Bin = "/usr/local/bin/codex"
	opts.WorkDir = "/tmp/codex-argv"
	opts.Prompt = "work order body"

	plan, err := CodexCommandPlan(opts)
	if err != nil {
		t.Fatalf("CodexCommandPlan: %v", err)
	}

	for _, bad := range []string{"--effort", "--max-budget-usd"} {
		for _, a := range plan.Argv {
			if a == bad {
				t.Fatalf("argv contains unsupported flag %q; full argv=%v", bad, plan.Argv)
			}
		}
	}

	// Reasoning effort must reach the CLI via -c model_reasoning_effort="..."
	// because `codex exec --help` does not list a dedicated --effort flag. Codex
	// config overrides use TOML values, so string efforts are quoted.
	var sawEffortOverride bool
	for i := 0; i < len(plan.Argv)-1; i++ {
		if plan.Argv[i] == "-c" && strings.HasPrefix(plan.Argv[i+1], "model_reasoning_effort=") {
			sawEffortOverride = true
			want := `model_reasoning_effort="` + opts.Effort + `"`
			if plan.Argv[i+1] != want {
				t.Fatalf("model_reasoning_effort override got %q want %q in argv=%v", plan.Argv[i+1], want, plan.Argv)
			}
		}
	}
	if !sawEffortOverride {
		t.Fatalf("expected `-c model_reasoning_effort=<value>` to deliver reasoning effort, got argv=%v", plan.Argv)
	}

	// Required base shape: codex exec must be invoked with the working
	// directory, sandbox, JSON event stream, and the trailing `-` stdin marker
	// the upstream CLI documents.
	wantBaseFlags := map[string]string{
		"--cd":      opts.WorkDir,
		"--sandbox": "workspace-write",
	}
	for flag, value := range wantBaseFlags {
		found := false
		for i := 0; i < len(plan.Argv)-1; i++ {
			if plan.Argv[i] == flag && plan.Argv[i+1] == value {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("argv missing %s=%s; full argv=%v", flag, value, plan.Argv)
		}
	}
	if plan.Argv[len(plan.Argv)-1] != "-" {
		t.Fatalf("argv must end with the `-` stdin marker; full argv=%v", plan.Argv)
	}

	// max_budget_usd has no Codex CLI equivalent, so the runner must surface
	// a warning rather than silently dropping the value.
	var sawBudgetWarning bool
	for _, w := range plan.Warnings {
		if strings.Contains(w, "max_budget_usd") {
			sawBudgetWarning = true
			break
		}
	}
	if !sawBudgetWarning {
		t.Fatalf("expected a max_budget_usd informational warning, got %#v", plan.Warnings)
	}
}

func TestCodexArgvWarnsWhenPromptModeAppendIsFlattened(t *testing.T) {
	t.Parallel()
	base := minimalCodexTask()
	base.Executor.PromptMode = "append"
	opts := CodexFromTask(base)
	opts.Prompt = "work order body"

	plan, err := CodexCommandPlan(opts)
	if err != nil {
		t.Fatalf("CodexCommandPlan: %v", err)
	}
	var sawPromptModeWarning bool
	for _, w := range plan.Warnings {
		if strings.Contains(w, "prompt_mode=append") && strings.Contains(w, "same effect as replace") {
			sawPromptModeWarning = true
			break
		}
	}
	if !sawPromptModeWarning {
		t.Fatalf("expected prompt_mode append warning, got %#v", plan.Warnings)
	}
}

func TestCodexArgvSandboxMapping(t *testing.T) {
	t.Parallel()
	cases := []struct {
		permission string
		want       string
	}{
		{"read-only", "read-only"},
		{"edit", "workspace-write"},
		{"sandbox-full-access", "danger-full-access"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.permission, func(t *testing.T) {
			base := minimalCodexTask()
			base.Scope.Permission = tc.permission
			opts := CodexFromTask(base)
			opts.Prompt = "p"
			plan, err := CodexCommandPlan(opts)
			if err != nil {
				t.Fatalf("CodexCommandPlan: %v", err)
			}
			var got string
			for i := 0; i < len(plan.Argv)-1; i++ {
				if plan.Argv[i] == "--sandbox" {
					got = plan.Argv[i+1]
					break
				}
			}
			if got != tc.want {
				t.Fatalf("sandbox mapping for permission=%q got %q want %q (argv=%v)", tc.permission, got, tc.want, plan.Argv)
			}
		})
	}
}
