package fileutil

import (
	"fmt"
	"os"
	"path/filepath"
)

// TryLock holds an OS file lock until close or process exit; the inode remains reusable.
// Callers supply a Galley-owned path: task.Queue builds it from the queue root
// plus a sha256 hex digest of the task ID, so no task input reaches it unencoded.
func TryLock(path string) (func(), error) {
	//nolint:gosec // G703: path is Galley-owned; see the contract above
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create lock dir %s: %w", filepath.Dir(path), err)
	}
	//nolint:gosec // G703: path is Galley-owned; see the contract above
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file %s: %w", path, err)
	}
	if err := lockFile(file); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	return func() { _ = file.Close() }, nil
}
