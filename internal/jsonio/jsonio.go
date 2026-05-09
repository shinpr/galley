// Package jsonio contains small JSON file helpers shared by Galley internals.
package jsonio

import (
	"encoding/json"
	"fmt"
	"os"
)

// Write writes value as indented JSON to path with owner-only permissions.
func Write(path string, value any) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	enc := json.NewEncoder(file)
	enc.SetIndent("", "  ")
	if err := enc.Encode(value); err != nil {
		if closeErr := file.Close(); closeErr != nil {
			return fmt.Errorf("write %s: %w; close: %v", path, err, closeErr)
		}
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close %s: %w", path, err)
	}
	return nil
}
