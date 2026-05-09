package taskstate

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/shinpr/galley/internal/task"
)

// Move writes the updated task, if supplied, and moves the YAML into tasks/<state>.
func Move(root, currentPath, state string, updated *task.Task) error {
	nextPath := filepath.Join(root, "tasks", state, filepath.Base(currentPath))
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

// FailMove records a terminal failure and preserves both primary and move errors.
func FailMove(root, runningPath string, updated *task.Task, primary error) error {
	if updated != nil {
		if updated.Status == "" || updated.Status == "running" {
			updated.Status = "failed"
		}
	}
	if moveErr := Move(root, runningPath, "failed", updated); moveErr != nil {
		fmt.Fprintf(os.Stderr, "galley: failed to move task %s to failed: %v (primary: %v)\n", runningPath, moveErr, primary)
		if primary == nil {
			return fmt.Errorf("failed to move task to failed: %w", moveErr)
		}
		return fmt.Errorf("%w; additionally failed to move task to failed: %v", primary, moveErr)
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
