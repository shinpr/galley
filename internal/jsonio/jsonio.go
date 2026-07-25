// Package jsonio contains small JSON file helpers shared by Galley internals.
package jsonio

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/shinpr/galley/internal/fileutil"
)

// Write writes value as indented JSON to path with owner-only permissions.
func Write(path string, value any) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(value); err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	return fileutil.WriteFileAtomic(path, buf.Bytes(), 0o600)
}
