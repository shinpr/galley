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
	err = WithExclusiveMarker(path+".lock", func() error {
		return stageAndMove(stagedWrite{Dir: dir, Path: path, Data: data, Perm: perm}, publishNoReplace)
	})
	var markerErr *markerExistsError
	if errors.As(err, &markerErr) {
		return fmt.Errorf("destination already exists: %s: %w", path, os.ErrExist)
	}
	return err
}

type markerExistsError struct {
	path string
}

func (e *markerExistsError) Error() string {
	return fmt.Sprintf("exclusive marker already exists at %s", e.path)
}

func (e *markerExistsError) Unwrap() error {
	return os.ErrExist
}

// WithExclusiveMarker runs fn while path exists as an exclusive marker.
func WithExclusiveMarker(path string, fn func() error) (err error) {
	marker, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return &markerExistsError{path: path}
		}
		return fmt.Errorf("create exclusive marker %s: %w", path, err)
	}
	if closeErr := marker.Close(); closeErr != nil {
		removeErr := removeMarker(path)
		return errors.Join(fmt.Errorf("close exclusive marker %s: %w", path, closeErr), removeErr)
	}
	defer func() {
		err = errors.Join(err, removeMarker(path))
	}()
	return fn()
}

func removeMarker(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove exclusive marker %s: %w", path, err)
	}
	return nil
}

func prepareWriteDir(path string) (string, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create dir %s: %w", dir, err)
	}
	return dir, nil
}

func stageAndRename(dir, path string, data []byte, perm os.FileMode) error {
	return stageAndMove(stagedWrite{Dir: dir, Path: path, Data: data, Perm: perm}, func(src, dst string) error {
		if err := os.Rename(src, dst); err != nil {
			return fmt.Errorf("rename temp file %s to %s: %w", src, dst, err)
		}
		return nil
	})
}

// stagedWrite is one file staged in dir and then moved into place.
type stagedWrite struct {
	Dir  string
	Path string
	Data []byte
	Perm os.FileMode
}

func stageAndMove(w stagedWrite, move func(string, string) error) error {
	dir, path, data, perm := w.Dir, w.Path, w.Data, w.Perm
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
	if err := move(tmpPath, path); err != nil {
		return err
	}
	cleanup = false
	SyncDir(dir)
	return nil
}
