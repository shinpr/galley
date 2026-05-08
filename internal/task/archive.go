package task

import (
	"fmt"
	"time"
)

// ArchiveOptions controls how a task is archived.
type ArchiveOptions struct {
	Reason string
}

// ArchiveResult describes the archived task move.
type ArchiveResult struct {
	Task Task   `json:"task"`
	From string `json:"from"`
	To   string `json:"to"`
}

// Archive moves a completed or reviewed task into tasks/archived without overwriting.
func Archive(path string, opts ArchiveOptions) (ArchiveResult, error) {
	loaded, err := Load(path)
	if err != nil {
		return ArchiveResult{}, err
	}
	switch loaded.Status {
	case "accepted", "pr_opened", "failed", "needs_supervisor_review", "merged", "closed":
	default:
		return ArchiveResult{}, fmt.Errorf("task %s status %q cannot be archived", loaded.ID, loaded.Status)
	}
	loaded.Status = "archived"
	loaded.Attempts = append(loaded.Attempts, Attempt{
		Number:            len(loaded.Attempts) + 1,
		StartedAt:         time.Now().UTC().Format(time.RFC3339Nano),
		CompletedAt:       time.Now().UTC().Format(time.RFC3339Nano),
		ClaudeStatus:      "not_run",
		SupervisorVerdict: "archived",
		Summary:           firstNonEmpty(opts.Reason, "Task archived."),
	})
	nextPath := siblingTaskPath(path, "archived")
	if nextPath == path {
		if err := Save(path, loaded); err != nil {
			return ArchiveResult{}, err
		}
	} else {
		if err := writeMovedTask(path, nextPath, loaded); err != nil {
			return ArchiveResult{}, err
		}
	}
	return ArchiveResult{Task: loaded, From: path, To: nextPath}, nil
}
