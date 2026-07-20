package task

import (
	"strings"
	"testing"

	"github.com/shinpr/galley/internal/profile"
)

func TestResolveEffectiveExecutorFieldPrecedence(t *testing.T) {
	t.Parallel()
	env := &profile.ExecutorDefault{
		DefaultCLI: "codex",
		Model:      "env-model",
		Effort:     "minimal",
	}
	cases := []struct {
		name string
		task Executor
		env  *profile.ExecutorDefault
		want Executor
	}{
		{
			name: "task wins each field independently",
			task: Executor{CLI: "claude", Model: "task-model", Effort: "max"},
			env:  env,
			want: Executor{CLI: "claude", Model: "task-model", Effort: "max"},
		},
		{
			name: "task cli keeps empty model and effort away from another provider's environment values",
			task: Executor{CLI: "claude"},
			env:  &profile.ExecutorDefault{DefaultCLI: "grok", Model: "grok-model", Effort: "none"},
			want: Executor{CLI: "claude", Model: "", Effort: ""},
		},
		{
			name: "task cli with model keeps empty effort away from environment effort",
			task: Executor{CLI: "claude", Model: "task-model"},
			env:  env,
			want: Executor{CLI: "claude", Model: "task-model", Effort: ""},
		},
		{
			name: "partial task override keeps sibling environment defaults",
			task: Executor{Model: "task-model"},
			env:  env,
			want: Executor{CLI: "codex", Model: "task-model", Effort: "minimal"},
		},
		{
			name: "environment fills all omitted fields",
			task: Executor{},
			env:  env,
			want: Executor{CLI: "codex", Model: "env-model", Effort: "minimal"},
		},
		{
			name: "built-in cli only; empty effort and model delegate to provider CLI",
			task: Executor{},
			env:  nil,
			want: Executor{CLI: DefaultExecutorCLI, Model: "", Effort: ""},
		},
		{
			name: "empty environment block keeps effort empty for the provider CLI",
			task: Executor{},
			env:  &profile.ExecutorDefault{},
			want: Executor{CLI: DefaultExecutorCLI, Model: "", Effort: ""},
		},
		{
			name: "environment cli without effort keeps effort empty",
			task: Executor{},
			env:  &profile.ExecutorDefault{DefaultCLI: "grok"},
			want: Executor{CLI: "grok", Model: "", Effort: ""},
		},
		{
			name: "task effort without cli uses environment cli",
			task: Executor{Effort: "xhigh"},
			env:  &profile.ExecutorDefault{DefaultCLI: "glm"},
			want: Executor{CLI: "glm", Model: "", Effort: "xhigh"},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ResolveEffectiveExecutor(tc.task, tc.env)
			if got != tc.want {
				t.Fatalf("ResolveEffectiveExecutor = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestValidateEffectiveExecutorRejectsInvalidPairs(t *testing.T) {
	t.Parallel()
	if err := ValidateEffectiveExecutor(Executor{CLI: "claude", Effort: "high"}); err != nil {
		t.Fatalf("valid pair must pass: %v", err)
	}
	for _, cli := range []string{"claude", "codex", "glm", "grok"} {
		if err := ValidateEffectiveExecutor(Executor{CLI: cli, Effort: ""}); err != nil {
			t.Fatalf("empty effort must delegate to %s CLI, got %v", cli, err)
		}
	}
	if err := ValidateEffectiveExecutor(Executor{CLI: "claude", Effort: "minimal"}); err == nil {
		t.Fatal("claude+minimal must fail")
	} else if !strings.Contains(err.Error(), "executor.effort for claude") {
		t.Fatalf("error must name provider effort set, got %v", err)
	}
	if err := ValidateEffectiveExecutor(Executor{CLI: "opus", Effort: "high"}); err == nil {
		t.Fatal("unknown cli must fail")
	}
}

func TestValidateExecutorOptionalFields(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		exec    Executor
		wantOK  bool
		wantErr string
	}{
		{name: "absent fields", exec: Executor{}, wantOK: true},
		{name: "cli only", exec: Executor{CLI: "codex"}, wantOK: true},
		{name: "model only", exec: Executor{Model: "any-model"}, wantOK: true},
		{name: "effort only union value", exec: Executor{Effort: "minimal"}, wantOK: true},
		{name: "complete config", exec: Executor{CLI: "claude", Model: "m", Effort: "high"}, wantOK: true},
		{name: "invalid cli", exec: Executor{CLI: "opus"}, wantErr: "executor.cli must be one of"},
		{name: "cli+effort mismatch", exec: Executor{CLI: "claude", Effort: "minimal"}, wantErr: "executor.effort for claude"},
		{name: "effort only unknown", exec: Executor{Effort: "turbo"}, wantErr: "executor.effort must be one of"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			base := validTask(t)
			base.Executor = tc.exec
			result := ValidateStructural(base)
			if tc.wantOK {
				if !result.Valid() {
					t.Fatalf("expected valid, got %#v", result.Errors)
				}
				return
			}
			if result.Valid() {
				t.Fatal("expected validation failure")
			}
			if !strings.Contains(strings.Join(result.Errors, "\n"), tc.wantErr) {
				t.Fatalf("errors %#v want substring %q", result.Errors, tc.wantErr)
			}
		})
	}
}

func TestWithExecutorDoesNotMutateOriginal(t *testing.T) {
	t.Parallel()
	original := validTask(t)
	original.Executor = Executor{CLI: "claude"}
	copyTask := WithExecutor(original, Executor{CLI: "codex", Effort: "minimal"})
	if original.Executor.CLI != "claude" {
		t.Fatalf("original mutated: %#v", original.Executor)
	}
	if copyTask.Executor.CLI != "codex" || copyTask.Executor.Effort != "minimal" {
		t.Fatalf("copy missing effective executor: %#v", copyTask.Executor)
	}
}
