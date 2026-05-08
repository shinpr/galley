package queue

import (
	"errors"
	"fmt"
	"io/fs"
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
func RunningRepoCounts(root string) map[string]int {
	counts := map[string]int{}
	matches, err := filepath.Glob(filepath.Join(root, "tasks", "running", "*.yaml"))
	if err != nil {
		return counts
	}
	for _, path := range matches {
		loaded, err := task.Load(path)
		if err != nil || loaded.Scope.CWD == "" {
			continue
		}
		counts[loaded.Scope.CWD]++
	}
	return counts
}

// ClaimTask atomically moves a queued task into running without overwriting.
func ClaimTask(root, queuedPath string) (string, error) {
	runningPath := filepath.Join(root, "tasks", "running", filepath.Base(queuedPath))
	lockPath := runningPath + ".lock"
	lockFile, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return "", fmt.Errorf("%w: claim %s lock already exists at %s", ErrClaimConflict, queuedPath, lockPath)
		}
		return "", fmt.Errorf("reserve claim %s: %w", queuedPath, err)
	}
	defer os.Remove(lockPath)
	if err := lockFile.Close(); err != nil {
		return "", fmt.Errorf("reserve claim %s: %w", queuedPath, err)
	}
	if _, err := os.Stat(runningPath); err == nil {
		return "", fmt.Errorf("%w: running task already exists at %s", ErrClaimConflict, runningPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("inspect running task %s: %w", runningPath, err)
	}
	if err := os.Rename(queuedPath, runningPath); err != nil {
		return "", fmt.Errorf("claim %s: %w", queuedPath, err)
	}
	return runningPath, nil
}

// RecoverStaleClaims requeues stale running tasks and removes stale claim locks.
func RecoverStaleClaims(root string, ttl time.Duration, now time.Time) error {
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
		if filepath.Ext(entry.Name()) == ".lock" {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("remove stale lock %s: %w", path, err)
			}
			continue
		}
		if entry.Type().IsRegular() && filepath.Ext(entry.Name()) == ".yaml" {
			if err := requeueRunningTask(root, path); err != nil {
				return err
			}
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
	file, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%w: destination exists at %s", fs.ErrExist, dst)
		}
		return err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(dst)
		return err
	}
	if err := os.Rename(src, dst); err != nil {
		_ = os.Remove(dst)
		return err
	}
	return nil
}
