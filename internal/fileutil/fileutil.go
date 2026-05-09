// Package fileutil contains small filesystem helpers shared across packages.
package fileutil

import "os"

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
