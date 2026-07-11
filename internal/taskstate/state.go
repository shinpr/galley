package taskstate

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/shinpr/galley/internal/task"
)

func moveToWorkflowState(root, currentPath string, state task.WorkflowState, updated *task.Task) error {
	nextPath := task.TaskStatePath(root, state, filepath.Base(currentPath))
	if updated != nil {
		if err := task.Save(currentPath, *updated); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(nextPath), 0o755); err != nil {
		return fmt.Errorf("create task state dir %s: %w", filepath.Dir(nextPath), err)
	}
	if err := renameNoOverwrite(currentPath, nextPath); err != nil {
		return fmt.Errorf("move task to %s: %w", state, err)
	}
	return nil
}

// MoveToStatus publishes a decoded task to the directory determined by its status.
func MoveToStatus(root, currentPath string, updated *task.Task) error {
	if updated == nil {
		return errors.New("move task to status: task is nil")
	}
	state, err := task.WorkflowStateForStatus(updated.Status)
	if err != nil {
		return err
	}
	return moveToWorkflowState(root, currentPath, state, updated)
}

// FailMoveToStatus records a decoded terminal failure and preserves both errors.
func FailMoveToStatus(root, runningPath string, updated *task.Task, primary error) error {
	if updated == nil {
		return errors.New("publish task failure: task is nil")
	}
	if updated.Status == "" || updated.Status == task.StatusRunning {
		updated.Status = task.StatusFailed
	}
	if moveErr := MoveToStatus(root, runningPath, updated); moveErr != nil {
		fmt.Fprintf(os.Stderr, "galley: failed to move task %s to failed: %v (primary: %v)\n", runningPath, moveErr, primary)
		if primary == nil {
			return fmt.Errorf("failed to move task to failed: %w", moveErr)
		}
		return errors.Join(primary, fmt.Errorf("additionally failed to move task to failed: %w", moveErr))
	}
	return primary
}

// RecoverUnreadableClaimToFailed preserves an undecodable claimed file while
// moving it to the fixed failed recovery state.
func RecoverUnreadableClaimToFailed(root, runningPath string, primary error) error {
	if moveErr := moveToWorkflowState(root, runningPath, task.WorkflowStateFailed, nil); moveErr != nil {
		fmt.Fprintf(os.Stderr, "galley: failed to recover unreadable task %s to failed: %v (primary: %v)\n", runningPath, moveErr, primary)
		if primary == nil {
			return fmt.Errorf("failed to recover unreadable task to failed: %w", moveErr)
		}
		return errors.Join(primary, fmt.Errorf("additionally failed to recover unreadable task to failed: %w", moveErr))
	}
	return primary
}

func renameNoOverwrite(src, dst string) error {
	lockPath := dst + ".lock"
	lock, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%w: destination is locked at %s", os.ErrExist, lockPath)
		}
		return err
	}
	if err := lock.Close(); err != nil {
		_ = os.Remove(lockPath)
		return err
	}
	defer func() {
		_ = os.Remove(lockPath)
	}()
	if _, err := os.Stat(dst); err == nil {
		return fmt.Errorf("%w: destination exists at %s", os.ErrExist, dst)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(src, dst)
}
