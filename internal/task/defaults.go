package task

import "github.com/shinpr/galley/internal/profile"

// DefaultLoopBudget is the retry budget used when a task omits execution_policy.loop_budget.
const DefaultLoopBudget = 10

// DefaultExecutorCLI is the fallback implementation backend.
const DefaultExecutorCLI = "claude"

// DefaultExecutorEffort is the fallback implementation effort.
const DefaultExecutorEffort = "high"

// ApplyDefaults fills task-owned defaults; executor fields resolve at run time.
func ApplyDefaults(t *Task) {
	if !t.ExecutionPolicy.LoopBudget.Set {
		t.ExecutionPolicy.LoopBudget = LoopBudget{Count: DefaultLoopBudget, Set: true}
	}
}

// Defaulted returns a copy of t with Galley execution defaults applied.
func Defaulted(t Task) Task {
	ApplyDefaults(&t)
	return t
}

// ResolveEffectiveExecutor applies task, environment, then built-in precedence per field.
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
	if effort == "" {
		effort = DefaultExecutorEffort
	}

	return Executor{CLI: cli, Model: model, Effort: effort}
}

// WithExecutor returns a copy of t with exec applied.
func WithExecutor(t Task, exec Executor) Task {
	t.Executor = exec
	return t
}
