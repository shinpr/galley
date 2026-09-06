//go:build windows

package fileutil

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

func renameNoReplace(src, dst string) error {
	srcPath, err := windowsMovePath(src)
	if err != nil {
		return err
	}
	dstPath, err := windowsMovePath(dst)
	if err != nil {
		return err
	}
	srcPtr, err := windows.UTF16PtrFromString(srcPath)
	if err != nil {
		return fmt.Errorf("encode source path %s: %w", srcPath, err)
	}
	dstPtr, err := windows.UTF16PtrFromString(dstPath)
	if err != nil {
		return fmt.Errorf("encode destination path %s: %w", dstPath, err)
	}
	if err := windows.MoveFile(srcPtr, dstPtr); err != nil {
		if errors.Is(err, windows.ERROR_ALREADY_EXISTS) || errors.Is(err, windows.ERROR_FILE_EXISTS) {
			return os.ErrExist
		}
		return fmt.Errorf("move %s to %s: %w", srcPath, dstPath, err)
	}
	return nil
}

func windowsMovePath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve absolute path %s: %w", path, err)
	}
	abs = filepath.Clean(abs)
	if strings.HasPrefix(abs, `\\?\`) || strings.HasPrefix(abs, `\??\`) || len(abs) < 248 {
		return abs, nil
	}
	if strings.HasPrefix(abs, `\\`) {
		return `\\?\UNC\` + abs[2:], nil
	}
	return `\\?\` + abs, nil
}
