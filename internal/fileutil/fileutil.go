// Package fileutil contains small filesystem helpers shared across packages.
package fileutil

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ExistsFile reports whether path exists and is not a directory.
func ExistsFile(path string) bool {
	stat, err := os.Stat(path)
	return err == nil && !stat.IsDir()
}

// SyncDir fsyncs a directory when the platform and filesystem support it.
func SyncDir(path string) {
	dir, err := os.Open(path)
	if err != nil {
		return
	}
	_ = dir.Sync()
	_ = dir.Close()
}

// WriteFileAtomic durably replaces path from a same-directory temp file.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir, err := prepareWriteDir(path)
	if err != nil {
		return err
	}
	return stageAndRename(dir, path, data, perm)
}

// WriteFileNoOverwriteAtomic publishes path atomically or wraps os.ErrExist.
func WriteFileNoOverwriteAtomic(path string, data []byte, perm os.FileMode) error {
	dir, err := prepareWriteDir(path)
	if err != nil {
		return err
	}
	lockPath := path + ".lock"
	lock, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%w: destination already exists: %s", os.ErrExist, path)
		}
		return fmt.Errorf("reserve %s: %w", lockPath, err)
	}
	if err := lock.Close(); err != nil {
		_ = os.Remove(lockPath)
		return fmt.Errorf("close reservation %s: %w", lockPath, err)
	}
	defer os.Remove(lockPath)

	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%w: destination already exists: %s", os.ErrExist, path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("check destination %s: %w", path, err)
	}
	return stageAndRename(dir, path, data, perm)
}

func prepareWriteDir(path string) (string, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create dir %s: %w", dir, err)
	}
	return dir, nil
}

func stageAndRename(dir, path string, data []byte, perm os.FileMode) error {
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
	SyncDir(dir)
	return nil
}
