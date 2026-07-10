package queue

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/shinpr/galley/internal/pathutil"
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

// isTaskYAMLName reports whether name is a task file (.yaml or .yml). Both
// extensions are accepted at queue-authoring time, so every consumer must match
// both or tasks authored with .yml are silently never claimed.
func isTaskYAMLName(name string) bool {
	switch filepath.Ext(name) {
	case ".yaml", ".yml":
		return true
	default:
		return false
	}
}

// taskYAMLFiles returns the task file glob matches (.yaml and .yml) directly
// under dir, preserving filepath.Glob semantics (missing dir yields no matches,
// non-regular entries are included).
func taskYAMLFiles(dir string) ([]string, error) {
	var matches []string
	for _, ext := range []string{"*.yaml", "*.yml"} {
		m, err := filepath.Glob(filepath.Join(dir, ext))
		if err != nil {
			return nil, err
		}
		matches = append(matches, m...)
	}
	return matches, nil
}

// QueuedTasks returns queued task YAML files in deterministic order.
func QueuedTasks(root string) ([]string, error) {
	matches, err := taskYAMLFiles(filepath.Join(root, "tasks", "queued"))
	if err != nil {
		return nil, err
	}
	sort.Strings(matches)
	return matches, nil
}

// RunningRepoCounts returns active running task counts keyed by source repository cwd.
//
// A single unreadable or malformed running file is skipped rather than failing
// the whole call: this function gates scheduling, and hard-failing on one bad
// file would stall every repo's queue. The key is canonicalized (symlinks
// resolved, and case-folded on case-insensitive default filesystems) so the same
// physical repo referenced by different path spellings counts once.
func RunningRepoCounts(root string) (map[string]int, error) {
	counts := map[string]int{}
	matches, err := taskYAMLFiles(filepath.Join(root, "tasks", "running"))
	if err != nil {
		return nil, err
	}
	for _, path := range matches {
		loaded, err := task.Load(path)
		if err != nil || loaded.Scope.CWD == "" {
			continue
		}
		counts[RepoConcurrencyKey(loaded.Scope.CWD)]++
	}
	return counts, nil
}

// RepoConcurrencyKey canonicalizes a repository cwd for per-repo concurrency
// accounting: it resolves symlinks and relative segments, and folds case on the
// platforms whose default filesystem is case-insensitive (Windows NTFS, macOS
// APFS) so /Repo and /repo are not counted as two repositories. Callers that
// look up counts returned by RunningRepoCounts must key with this function.
func RepoConcurrencyKey(cwd string) string {
	key := pathutil.CleanPhysical(cwd)
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		key = strings.ToLower(key)
	}
	return key
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
		if entry.Type().IsRegular() && isTaskYAMLName(entry.Name()) {
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
	_ = RemoveOwner(runningPath)
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
