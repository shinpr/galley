//go:build darwin

package fileutil

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

func renameNoReplace(src, dst string) error {
	err := unix.RenamexNp(src, dst, unix.RENAME_EXCL)
	if errors.Is(err, unix.ENOTSUP) {
		return errNoReplaceUnsupported
	}
	if err != nil {
		return fmt.Errorf("renamex_np %s to %s: %w", src, dst, err)
	}
	return nil
}
