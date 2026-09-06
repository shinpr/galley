//go:build !windows

package fileutil

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func lockFile(file *os.File) error {
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return fmt.Errorf("flock: %w", err)
	}
	return nil
}
