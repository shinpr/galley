package daemoncmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/shinpr/galley/internal/daemonctl"
	"github.com/shinpr/galley/internal/queue"
	"github.com/shinpr/galley/internal/task"
)

const forceStopInterruptionMessage = "Daemon force stop interrupted the active attempt."

func failOwnedRunningTasks(root string, target daemonctl.PIDFile) (int, error) {
	runningDir := task.TaskStateDir(root, task.WorkflowStateRunning)
	entries, err := os.ReadDir(runningDir)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("inspect running tasks: %w", err)
	}
	failed := 0
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !isTaskYAML(entry.Name()) {
			continue
		}
		runningPath := filepath.Join(runningDir, entry.Name())
		owner, err := queue.ReadOwner(runningPath)
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, queue.ErrInvalidOwner) {
			continue
		}
		if err != nil {
			return failed, fmt.Errorf("inspect task owner %s: %w", runningPath, err)
		}
		if !ownerMatchesDaemon(owner, target) {
			continue
		}
		if err := failRunningTask(root, runningPath); err != nil {
			return failed, err
		}
		failed++
	}
	return failed, nil
}

func isTaskYAML(name string) bool {
	ext := filepath.Ext(name)
	return ext == ".yaml" || ext == ".yml"
}

func ownerMatchesDaemon(owner queue.Owner, target daemonctl.PIDFile) bool {
	if owner.PID != target.PID {
		return false
	}
	return owner.ProcessStartedAt == "" || target.ProcessStartedAt == "" || owner.ProcessStartedAt == target.ProcessStartedAt
}

func failRunningTask(root, runningPath string) error {
	loaded, err := task.Load(runningPath)
	if err != nil {
		return fmt.Errorf("load force-stopped task %s: %w", runningPath, err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	loaded.Status = task.StatusFailed
	loaded.Attempts = append(loaded.Attempts, task.Attempt{
		Number:            len(loaded.Attempts) + 1,
		StartedAt:         now,
		CompletedAt:       now,
		ClaudeStatus:      "interrupted",
		SupervisorVerdict: "not_reviewed",
		Summary:           forceStopInterruptionMessage,
		Error: &task.AttemptError{
			Phase:   "daemon",
			Kind:    "daemon_force_stopped",
			Message: forceStopInterruptionMessage,
		},
	})
	failedPath := task.TaskStatePath(root, task.WorkflowStateFailed, filepath.Base(runningPath))
	if err := task.WriteMovedTask(runningPath, failedPath, loaded); err != nil {
		return fmt.Errorf("publish force-stopped task %s: %w", runningPath, err)
	}
	if err := queue.RemoveOwner(runningPath); err != nil {
		fmt.Fprintf(os.Stderr, "galley: remove orphaned force-stop owner %s: %v\n", queue.OwnerPath(runningPath), err)
	}
	return nil
}
