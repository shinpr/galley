// Package jsonio contains small JSON file helpers shared by Galley internals.
package jsonio

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/shinpr/galley/internal/fileutil"
)

// Write writes value as indented JSON to path with owner-only permissions.
func Write(path string, value any) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	file, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file for %s: %w", path, err)
	}
	tmpPath := file.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return fmt.Errorf("chmod temp file %s: %w", tmpPath, err)
	}
	enc := json.NewEncoder(file)
	enc.SetIndent("", "  ")
	if err := enc.Encode(value); err != nil {
		if closeErr := file.Close(); closeErr != nil {
			return fmt.Errorf("write %s: %w; close: %v", path, err, closeErr)
		}
		return fmt.Errorf("write %s: %w", path, err)
	}
	// Flush the file's data to disk before the rename so a crash cannot leave a
	// durable rename pointing at a truncated/zero-length evidence file.
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync %s: %w", tmpPath, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close %s: %w", path, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename temp file %s to %s: %w", tmpPath, path, err)
	}
	cleanup = false
	fileutil.SyncDir(dir)
	return nil
}
