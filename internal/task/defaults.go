package task

import "github.com/shinpr/galley/internal/profile"

// DefaultLoopBudget is the retry budget used when a task omits execution_policy.loop_budget.
const DefaultLoopBudget = 10

// DefaultMode is the sole supported task execution mode.
const DefaultMode = "afk"

// DefaultAFKDecisionPolicy is the fixed ambiguity policy rendered in AFK work orders.
const DefaultAFKDecisionPolicy = "choose-smallest-reversible"

// DefaultExecutorCLI is the fallback implementation backend.
const DefaultExecutorCLI = "claude"

// ApplyDefaults resolves fixed authoring values before validation, display, and
// queue decisions. Executor defaults remain runtime-owned.
func ApplyDefaults(t *Task) {
	if t.Mode == "" {
		t.Mode = DefaultMode
	}
	if t.Status == "" {
		t.Status = StatusDraft
	}
	if !t.ExecutionPolicy.LoopBudget.Set {
		t.ExecutionPolicy.LoopBudget = LoopBudget{Count: DefaultLoopBudget, Set: true}
	}
	// AFK execution always uses an isolated worktree. bool cannot distinguish
	// omit from false, so the fixed path always enables worktree isolation.
	if t.Mode == DefaultMode {
		t.Worktree.Enabled = true
	}
}

// Defaulted returns a copy of t with Galley execution defaults applied.
func Defaulted(t Task) Task {
	ApplyDefaults(&t)
	return t
}

// ResolveEffectiveExecutor applies task, then environment precedence per field.
// Only cli has a Galley built-in fallback; empty model and effort stay empty so
// the selected provider CLI owns model and reasoning-effort selection.
func ResolveEffectiveExecutor(taskExec Executor, env *profile.ExecutorDefault) Executor {
	cli := taskExec.CLI
	if cli == "" && env != nil {
		cli = env.DefaultCLI
	}
	if cli == "" {
		cli = DefaultExecutorCLI
	}

	model := taskExec.Model
	if model == "" && env != nil {
		model = env.Model
	}

	effort := taskExec.Effort
	if effort == "" && env != nil {
		effort = env.Effort
	}

	return Executor{CLI: cli, Model: model, Effort: effort}
}

// WithExecutor returns a copy of t with exec applied.
func WithExecutor(t Task, exec Executor) Task {
	t.Executor = exec
	return t
}
