package task

// DefaultLoopBudget is the retry budget used when a task omits execution_policy.loop_budget.
const DefaultLoopBudget = 10

// ApplyDefaults fills optional task fields with the values Galley will execute.
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
