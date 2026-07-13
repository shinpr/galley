package runner

import "github.com/shinpr/galley/internal/task"

type executorTaskOptions struct {
	Model   string
	Effort  string
	WorkDir string
}

func executorOptionsFromTask(t task.Task) executorTaskOptions {
	return executorTaskOptions{
		Model:   t.Executor.Model,
		Effort:  t.Executor.Effort,
		WorkDir: t.Scope.CWD,
	}
}
