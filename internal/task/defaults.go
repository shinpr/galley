package task

import "github.com/shinpr/galley/internal/profile"

// DefaultLoopBudget is the retry budget used when a task omits execution_policy.loop_budget.
const DefaultLoopBudget = 10

// DefaultExecutorCLI is the built-in implementation backend when neither the
// task nor the repository environment profile selects one.
const DefaultExecutorCLI = "claude"

// DefaultExecutorEffort is the built-in reasoning effort when neither the task
// nor the repository environment profile selects one.
const DefaultExecutorEffort = "high"

// ApplyDefaults fills optional task fields with the values Galley will execute.
// Executor CLI/model/effort are resolved separately at run start so environment
// profile changes remain authoritative without rewriting task YAML.
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

// ResolveEffectiveExecutor resolves each executor field independently using
// explicit task value, then current environment profile value, then the
// built-in default. It does not mutate the authored task or environment.
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

// WithExecutor returns a copy of t whose Executor is replaced. Used to apply
// run-resolved executor settings without writing them back as task overrides.
func WithExecutor(t Task, exec Executor) Task {
	t.Executor = exec
	return t
}
