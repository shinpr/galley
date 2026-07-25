//go:build darwin

package fileutil

import (
	"errors"

	"golang.org/x/sys/unix"
)

func renameNoReplace(src, dst string) error {
	err := unix.RenamexNp(src, dst, unix.RENAME_EXCL)
	if errors.Is(err, unix.ENOTSUP) {
		return errNoReplaceUnsupported
	}
	return err
}
