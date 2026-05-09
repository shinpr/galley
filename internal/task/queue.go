package task

import (
	"fmt"
	"time"
)

type QueueOptions struct {
	Reason string
}

type QueueResult struct {
	Task Task   `json:"task"`
	From string `json:"from"`
	To   string `json:"to"`
}

// Queue validates a task and moves it to tasks/queued without overwriting an existing queued task.
func Queue(path string, opts QueueOptions) (QueueResult, error) {
	loaded, err := Load(path)
	if err != nil {
		return QueueResult{}, err
	}
	if loaded.Status != "draft" {
		return QueueResult{}, fmt.Errorf("task %s status %q cannot be queued with task queue; use task requeue for reviewed tasks", loaded.ID, loaded.Status)
	}
	ResolveFileSources(path, &loaded)
	ApplyDefaults(&loaded)
	loaded.Status = "queued"
	validation := Validate(loaded)
	if !validation.Valid() {
		return QueueResult{}, fmt.Errorf("task validation failed: %v", validation.Errors)
	}
	loaded.Attempts = append(loaded.Attempts, Attempt{
		Number:            len(loaded.Attempts) + 1,
		StartedAt:         time.Now().UTC().Format(time.RFC3339Nano),
		CompletedAt:       time.Now().UTC().Format(time.RFC3339Nano),
		ClaudeStatus:      "not_run",
		SupervisorVerdict: "queued",
		Summary:           firstNonEmpty(opts.Reason, "Task queued for daemon execution."),
	})
	nextPath := queuedPathFor(path)
	if nextPath == path {
		if err := Save(path, loaded); err != nil {
			return QueueResult{}, err
		}
	} else {
		if err := writeMovedTask(path, nextPath, loaded); err != nil {
			return QueueResult{}, err
		}
	}
	return QueueResult{Task: loaded, From: path, To: nextPath}, nil
}
