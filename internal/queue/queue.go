package queue

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/shinpr/galley/internal/task"
)

// ErrClaimConflict indicates another daemon or stale running file owns the task.
var ErrClaimConflict = errors.New("claim conflict")

// EnsureLayout creates the file-backed queue directory layout.
func EnsureLayout(root string) error {
	for _, path := range []string{
		filepath.Join(root, "tasks", "queued"),
		filepath.Join(root, "tasks", "draft"),
		filepath.Join(root, "tasks", "running"),
		filepath.Join(root, "tasks", "done"),
		filepath.Join(root, "tasks", "failed"),
		filepath.Join(root, "tasks", "archived"),
		filepath.Join(root, "runs"),
	} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", path, err)
		}
	}
	return nil
}

// QueuedTasks returns queued task YAML files in deterministic order.
func QueuedTasks(root string) ([]string, error) {
	pattern := filepath.Join(root, "tasks", "queued", "*.yaml")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, err
	}
	sort.Strings(matches)
	return matches, nil
}

// RunningRepoCounts returns active running task counts keyed by source repository cwd.
func RunningRepoCounts(root string) (map[string]int, error) {
	counts := map[string]int{}
	matches, err := filepath.Glob(filepath.Join(root, "tasks", "running", "*.yaml"))
	if err != nil {
		return nil, err
	}
	for _, path := range matches {
		loaded, err := task.Load(path)
		if err != nil {
			return nil, fmt.Errorf("load running task %s: %w", path, err)
		}
		if loaded.Scope.CWD == "" {
			return nil, fmt.Errorf("running task %s has empty scope.cwd", path)
		}
		counts[loaded.Scope.CWD]++
	}
	return counts, nil
}

// ClaimTask claims a queued task into running without overwriting an existing running task.
func ClaimTask(root, queuedPath string) (string, error) {
	runningPath := filepath.Join(root, "tasks", "running", filepath.Base(queuedPath))
	if err := noOverwriteRename(queuedPath, runningPath); err != nil {
		if errors.Is(err, os.ErrExist) {
			return "", fmt.Errorf("%w: running task already exists at %s", ErrClaimConflict, runningPath)
		}
		return "", fmt.Errorf("claim %s: %w", queuedPath, err)
	}
	return runningPath, nil
}

// RecoverStaleClaims requeues stale running tasks and removes stale claim locks.
func RecoverStaleClaims(root string, ttl time.Duration, now time.Time) error {
	for _, dir := range []string{
		filepath.Join(root, "tasks", "queued"),
		filepath.Join(root, "tasks", "running"),
	} {
		if err := removeStaleLocks(dir, ttl, now); err != nil {
			return err
		}
	}

	runningDir := filepath.Join(root, "tasks", "running")
	entries, err := os.ReadDir(runningDir)
	if err != nil {
		return fmt.Errorf("read running dir %s: %w", runningDir, err)
	}
	for _, entry := range entries {
		path := filepath.Join(runningDir, entry.Name())
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("stat %s: %w", path, err)
		}
		if now.Sub(info.ModTime()) < ttl {
			continue
		}
		if entry.Type().IsRegular() && filepath.Ext(entry.Name()) == ".yaml" {
			if err := requeueRunningTask(root, path); err != nil {
				if errors.Is(err, os.ErrExist) {
					continue
				}
				return err
			}
		}
	}
	return nil
}

func removeStaleLocks(dir string, ttl time.Duration, now time.Time) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read task dir %s: %w", dir, err)
	}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".lock" {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("stat %s: %w", path, err)
		}
		if now.Sub(info.ModTime()) < ttl {
			continue
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove stale lock %s: %w", path, err)
		}
	}
	return nil
}

func requeueRunningTask(root, runningPath string) error {
	loaded, err := task.Load(runningPath)
	if err != nil {
		return fmt.Errorf("load stale running task %s: %w", runningPath, err)
	}
	loaded.Status = "queued"
	if err := task.Save(runningPath, loaded); err != nil {
		return err
	}
	queuedPath := filepath.Join(root, "tasks", "queued", filepath.Base(runningPath))
	if err := noOverwriteRename(runningPath, queuedPath); err != nil {
		return fmt.Errorf("requeue stale task %s: %w", runningPath, err)
	}
	return nil
}

func noOverwriteRename(src, dst string) error {
	// Claim locks are represented by exclusive marker files. The descriptor can
	// close after creation; ownership is the marker's presence until defer removes it.
	srcLockPath := src + ".lock"
	srcLock, err := createClaimLock(srcLockPath)
	if err != nil {
		return err
	}
	defer removeClaimLock(srcLockPath)
	if err := srcLock.Close(); err != nil {
		return err
	}

	dstLockPath := dst + ".lock"
	dstLock, err := createClaimLock(dstLockPath)
	if err != nil {
		return err
	}
	defer removeClaimLock(dstLockPath)
	if err := dstLock.Close(); err != nil {
		return err
	}

	if _, err := os.Stat(dst); err == nil {
		return fmt.Errorf("%w: destination exists at %s", os.ErrExist, dst)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(src, dst)
}

func createClaimLock(path string) (*os.File, error) {
	lock, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("%w: task is locked at %s", os.ErrExist, path)
		}
		return nil, err
	}
	return lock, nil
}

func removeClaimLock(path string) {
	_ = os.Remove(path)
}
