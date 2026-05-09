package claude_guard_plugin

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// FS contains the session-only Claude plugin used to guard Galley executor runs.
//
//go:embed .claude-plugin/plugin.json hooks/hooks.json scripts/block-finalizer-commands.py scripts/require-final-json.py
var FS embed.FS

// Ensure writes the embedded guard plugin to dst and returns dst.
func Ensure(dst string) (string, error) {
	files := []string{
		".claude-plugin/plugin.json",
		"hooks/hooks.json",
		"scripts/block-finalizer-commands.py",
		"scripts/require-final-json.py",
	}
	version, err := contentHash(files)
	if err != nil {
		return "", err
	}
	dst = filepath.Join(dst, version)
	if _, err := os.Stat(filepath.Join(dst, ".complete")); err == nil {
		return dst, nil
	} else if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("inspect guard plugin dir %s: %w", dst, err)
	}
	parent := filepath.Dir(dst)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return "", fmt.Errorf("create guard plugin parent dir %s: %w", parent, err)
	}
	staging, err := os.MkdirTemp(parent, "."+filepath.Base(dst)+".tmp-")
	if err != nil {
		return "", fmt.Errorf("create guard plugin staging dir: %w", err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(staging)
		}
	}()
	for _, name := range files {
		data, err := FS.ReadFile(name)
		if err != nil {
			return "", fmt.Errorf("read embedded guard plugin file %s: %w", name, err)
		}
		path := filepath.Join(staging, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return "", fmt.Errorf("create guard plugin dir %s: %w", filepath.Dir(path), err)
		}
		mode := fs.FileMode(0o600)
		if strings.HasPrefix(name, "scripts/") {
			mode = 0o700
		}
		if err := os.WriteFile(path, data, mode); err != nil {
			return "", fmt.Errorf("write guard plugin file %s: %w", path, err)
		}
	}
	if err := os.WriteFile(filepath.Join(staging, ".complete"), []byte(version+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("write guard plugin marker: %w", err)
	}
	if err := os.Rename(staging, dst); err != nil {
		if _, statErr := os.Stat(filepath.Join(dst, ".complete")); statErr == nil {
			return dst, nil
		}
		return "", fmt.Errorf("publish guard plugin dir %s: %w", dst, err)
	}
	cleanup = false
	return dst, nil
}

func contentHash(files []string) (string, error) {
	hash := sha256.New()
	for _, name := range files {
		data, err := FS.ReadFile(name)
		if err != nil {
			return "", fmt.Errorf("read embedded guard plugin file %s: %w", name, err)
		}
		hash.Write([]byte(name))
		hash.Write([]byte{0})
		hash.Write(data)
		hash.Write([]byte{0})
	}
	return "sha256-" + hex.EncodeToString(hash.Sum(nil))[:16], nil
}
