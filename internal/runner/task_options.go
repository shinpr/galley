package runner

import "github.com/shinpr/galley/internal/task"

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
