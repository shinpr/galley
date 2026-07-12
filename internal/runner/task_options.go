package runner

import "github.com/shinpr/galley/internal/task"

// galleyPromptMode is the provider prompt transport Galley always uses.
// Tasks no longer configure prompt_mode; replace is the only supported path.
const galleyPromptMode = "replace"

type executorTaskOptions struct {
	Model      string
	Effort     string
	PromptMode string
	WorkDir    string
}

func executorOptionsFromTask(t task.Task) executorTaskOptions {
	return executorTaskOptions{
		Model:      t.Executor.Model,
		Effort:     t.Executor.Effort,
		PromptMode: galleyPromptMode,
		WorkDir:    t.Scope.CWD,
	}
}
