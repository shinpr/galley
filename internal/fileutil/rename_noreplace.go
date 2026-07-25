package fileutil

import (
	"errors"
	"fmt"
	"os"
)

var errNoReplaceUnsupported = errors.New("filesystem does not support rename without replacement")

// RenameNoReplaceUnderMarker atomically moves src while the caller holds dst's marker.
func RenameNoReplaceUnderMarker(src, dst string) error {
	err := renameNoReplace(src, dst)
	if errors.Is(err, errNoReplaceUnsupported) {
		err = renameUnderMarkerFallback(src, dst)
	}
	return normalizeNoReplaceError(dst, err)
}

func publishNoReplace(src, dst string) error {
	err := renameNoReplace(src, dst)
	if errors.Is(err, errNoReplaceUnsupported) {
		err = linkAndUnlinkNoReplace(src, dst)
	}
	return normalizeNoReplaceError(dst, err)
}

func normalizeNoReplaceError(dst string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, os.ErrExist) {
		return fmt.Errorf("destination already exists: %s: %w", dst, os.ErrExist)
	}
	return fmt.Errorf("move to %s without replacement: %w", dst, err)
}

func renameUnderMarkerFallback(src, dst string) error {
	if _, err := os.Lstat(dst); err == nil {
		return os.ErrExist
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(src, dst)
}

func linkAndUnlinkNoReplace(src, dst string) error {
	if err := os.Link(src, dst); err != nil {
		return err
	}
	if err := os.Remove(src); err != nil {
		rollbackErr := os.Remove(dst)
		if rollbackErr != nil {
			rollbackErr = fmt.Errorf("remove fallback destination %s: %w", dst, rollbackErr)
		}
		return errors.Join(fmt.Errorf("remove linked source %s: %w", src, err), rollbackErr)
	}
	return nil
}
