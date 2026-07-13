package task

import "github.com/shinpr/galley/internal/profile"

// DefaultLoopBudget is the retry budget used when a task omits execution_policy.loop_budget.
const DefaultLoopBudget = 10

// DefaultMode is the sole supported task execution mode.
const DefaultMode = "afk"

// DefaultAFKDecisionPolicy is the fixed AFK ambiguity policy applied when a
// draft omits execution_policy.afk_decision_policy (the field is no longer
// authored; work orders still surface this constant).
const DefaultAFKDecisionPolicy = "choose-smallest-reversible"

// DefaultExecutorCLI is the fallback implementation backend.
const DefaultExecutorCLI = "claude"

// DefaultExecutorEffort is the fallback implementation effort.
const DefaultExecutorEffort = "high"

// ApplyDefaults fills fixed and omitted authoring values needed before
// validation, display, queue eligibility, and command decisions. Executor
// fields still resolve at run time; daemon-owned lifecycle fields are not
// invented here.
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
