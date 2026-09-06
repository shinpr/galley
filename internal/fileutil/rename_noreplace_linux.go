//go:build linux

package fileutil

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

func renameNoReplace(src, dst string) error {
	err := unix.Renameat2(unix.AT_FDCWD, src, unix.AT_FDCWD, dst, unix.RENAME_NOREPLACE)
	if errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EOPNOTSUPP) {
		return errNoReplaceUnsupported
	}
	if err != nil {
		return fmt.Errorf("renameat2 %s to %s: %w", src, dst, err)
	}
	return nil
}
