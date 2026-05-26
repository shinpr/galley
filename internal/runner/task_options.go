package runner

import "github.com/shinpr/galley/internal/task"

type executorTaskOptions struct {
	Model        string
	Effort       string
	PromptMode   string
	MaxBudgetUSD float64
	WorkDir      string
}

func executorOptionsFromTask(t task.Task) executorTaskOptions {
	promptMode := t.Executor.PromptMode
	if promptMode == "" {
		promptMode = "replace"
	}
	return executorTaskOptions{
		Model:        t.Executor.Model,
		Effort:       t.Executor.Effort,
		PromptMode:   promptMode,
		MaxBudgetUSD: t.Executor.MaxBudgetUSDValue(),
		WorkDir:      t.Scope.CWD,
	}
}
