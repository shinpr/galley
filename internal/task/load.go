package task

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/shinpr/galley/internal/fileutil"
	"go.yaml.in/yaml/v3"
)

// Load reads a task YAML file and decodes it with strict field checking.
//
// Load is the strict path used by active task intake and execution: `galley
// task validate`, `galley task queue`, `galley task requeue`, archive's
// current-schema path, and daemon execution of a queued task. Unknown fields
// or type mismatches surface as decode errors so a malformed active task is
// rejected before it reaches the executor. Read-only inspection of legacy or
// historical task files that may contain fields from previous schema
// revisions must use LoadLenient instead.
func Load(path string) (Task, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Task{}, fmt.Errorf("read %s: %w", path, err)
	}

	var task Task
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&task); err != nil {
		return Task{}, fmt.Errorf("decode %s: %w", path, err)
	}

	return task, nil
}

// LoadLenient reads a task YAML file with unknown fields tolerated. It is
// intended for read-only inspection of legacy or historical task files such
// as `galley task list` and `galley task show` scans, daemon helper sweeps
// over `tasks/done` and `tasks/failed`, and archive's safe-status fallback.
// Callers must not use a lenient-loaded Task to overwrite the file through
// task.Save or task.Requeue: re-marshalling the struct strips fields the
// current schema does not know about, which would silently mutate audit
// history. Active task intake and execution must continue to call Load.
//
// LoadLenient still surfaces type-mismatch errors (for example a
// loop_budget value that is not an integer) because the matching custom
// UnmarshalYAML implementations validate value shape regardless of the
// KnownFields setting. Callers should treat a returned error as a non-fatal
// "this file is unreadable" signal and continue processing the rest of the
// scan rather than aborting the whole command.
func LoadLenient(path string) (Task, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Task{}, fmt.Errorf("read %s: %w", path, err)
	}

	var task Task
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(false)
	if err := decoder.Decode(&task); err != nil {
		return Task{}, fmt.Errorf("decode %s: %w", path, err)
	}

	return task, nil
}

// LoadAndValidate loads a task and runs structural and environment validation.
func LoadAndValidate(path string) (ValidationResult, error) {
	loaded, err := Load(path)
	if err != nil {
		return ValidationResult{}, err
	}
	ApplyDefaults(&loaded)
	result := Validate(loaded)
	result.Task = loaded
	return result, nil
}

// Save writes a task YAML file.
func Save(path string, value Task) error {
	data, err := yaml.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	if err := writeFileAtomic(path, data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	return writeFileAtomicMode(path, data, perm, true)
}

func writeFileNoOverwriteAtomic(path string, data []byte, perm os.FileMode) error {
	return writeFileAtomicMode(path, data, perm, false)
}

func writeFileAtomicMode(path string, data []byte, perm os.FileMode, overwrite bool) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create dir %s: %w", dir, err)
	}
	if !overwrite {
		// Cross-platform no-overwrite publication boundary. The destination
		// task YAML must appear atomically: a concurrently polling daemon
		// must see either no file at all or the fully written/synced file.
		// The previous hardlink-based primitive was replaced because
		// `os.Link` used CreateHardLink on Windows and failed on
		// filesystems without hardlink support (FAT32, ReFS, cross-volume
		// temp dirs). A naive write directly to the destination with
		// O_CREATE|O_EXCL preserved duplicate-destination protection but
		// reintroduced a partial-publication window: the destination
		// becomes visible immediately after open(), before the YAML bytes
		// are written and synced, so a fast poller could load a truncated
		// file and fail the task with a decode error.
		//
		// The reservation-then-rename pattern below restores atomicity
		// without hardlinks. A separate `<path>.lock` file created with
		// O_CREATE|O_EXCL serializes publication for `path`, the YAML is
		// written to a temp file in the same directory and fsynced, the
		// final destination is re-checked while we hold the reservation,
		// and `os.Rename` publishes the fully written file in a single
		// step. Same-directory rename is atomic on every supported OS.
		// The lock is removed only after a successful publication, so
		// callers that observe a contended lock can safely retry: either
		// the destination is now in place (treated as duplicate-
		// destination by the caller surface) or the previous writer
		// failed and the lock has been cleaned up.
		lockPath := path + ".lock"
		lock, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			if errors.Is(err, os.ErrExist) {
				// A concurrent writer holds the reservation for this
				// destination. From the caller's perspective (`task
				// queue`, `task requeue`, archive, daemon claim/requeue)
				// this is indistinguishable from a duplicate-destination
				// conflict: in both cases another path-owner reached
				// here first.
				return fmt.Errorf("destination already exists: %s", path)
			}
			return fmt.Errorf("reserve %s: %w", lockPath, err)
		}
		if err := lock.Close(); err != nil {
			_ = os.Remove(lockPath)
			return fmt.Errorf("close reservation %s: %w", lockPath, err)
		}
		lockHeld := true
		defer func() {
			if lockHeld {
				_ = os.Remove(lockPath)
			}
		}()

		// Re-check the destination after acquiring the reservation. The
		// reservation guards against two writers publishing the same
		// path; the existence check guards against a previously
		// completed publication.
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("destination already exists: %s", path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("check destination %s: %w", path, err)
		}

		tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
		if err != nil {
			return fmt.Errorf("create temp file for %s: %w", path, err)
		}
		tmpPath := tmp.Name()
		cleanupTmp := true
		defer func() {
			if cleanupTmp {
				_ = os.Remove(tmpPath)
			}
		}()
		if err := tmp.Chmod(perm); err != nil {
			_ = tmp.Close()
			return fmt.Errorf("chmod temp file %s: %w", tmpPath, err)
		}
		if _, err := tmp.Write(data); err != nil {
			_ = tmp.Close()
			return fmt.Errorf("write temp file %s: %w", tmpPath, err)
		}
		if err := tmp.Sync(); err != nil {
			_ = tmp.Close()
			return fmt.Errorf("sync temp file %s: %w", tmpPath, err)
		}
		if err := tmp.Close(); err != nil {
			return fmt.Errorf("close temp file %s: %w", tmpPath, err)
		}
		if err := os.Rename(tmpPath, path); err != nil {
			return fmt.Errorf("rename temp file %s to %s: %w", tmpPath, path, err)
		}
		cleanupTmp = false
		fileutil.SyncDir(dir)
		// Successful publication: remove the reservation explicitly so a
		// concurrent caller that observed the lock retries against the
		// now-present destination and surfaces the duplicate-destination
		// error rather than a reservation conflict.
		if err := os.Remove(lockPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove reservation %s: %w", lockPath, err)
		}
		lockHeld = false
		return nil
	}

	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file for %s: %w", path, err)
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod temp file %s: %w", tmpPath, err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp file %s: %w", tmpPath, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temp file %s: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename temp file %s to %s: %w", tmpPath, path, err)
	}
	cleanup = false
	fileutil.SyncDir(dir)
	return nil
}
