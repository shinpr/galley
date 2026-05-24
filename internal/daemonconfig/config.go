// Package daemonconfig owns the durable daemon startup defaults written to
// daemon.yaml under the selected daemon root. It is intentionally independent
// from the daemon and daemoncmd packages so daemon CLI wiring and the runtime
// loop can both consume the same on-disk contract without creating an import
// cycle.
//
// Resolution order, per the daemon startup defaults task contract:
//
//  1. Daemon CLI startup options (explicit flags on `galley daemon run` or
//     `galley daemon start`).
//  2. daemon.yaml under the selected daemon root.
//  3. Built-in defaults (Codex supervisor and the other documented daemon
//     defaults).
//
// Repository-level `environment.yaml` supervisor.default_cli is a *task-level*
// override that takes precedence over all three of the above for that task's
// supervisor selection only. That precedence is enforced by the daemon
// runtime, not this package.
package daemonconfig

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"go.yaml.in/yaml/v3"
)

// Filename is the daemon configuration filename written under the daemon root.
const Filename = "daemon.yaml"

// File is the YAML shape persisted under <root>/daemon.yaml. Every field is
// optional: an absent field defers to the next layer in the resolution chain.
// Durations are stored as Go duration strings (for example "10s" or "30m") so
// the file stays human-readable.
type File struct {
	Supervisor           string `yaml:"supervisor,omitempty"`
	MaxConcurrentTasks   *int   `yaml:"max_concurrent_tasks,omitempty"`
	MaxConcurrentPerRepo *int   `yaml:"max_concurrent_per_repo,omitempty"`
	PollInterval         string `yaml:"poll_interval,omitempty"`
	ClaimTTL             string `yaml:"claim_ttl,omitempty"`
	HeartbeatInterval    string `yaml:"heartbeat_interval,omitempty"`
	ShutdownTimeout      string `yaml:"shutdown_timeout,omitempty"`
	IdleTimeout          string `yaml:"idle_timeout,omitempty"`
}

// Defaults returns the documented daemon startup defaults that are written to
// daemon.yaml on first creation. They mirror the built-in CLI default values
// so an operator can edit a single file instead of relying on flag defaults.
func Defaults() File {
	one := 1
	return File{
		Supervisor:           "codex",
		MaxConcurrentTasks:   &one,
		MaxConcurrentPerRepo: &one,
		PollInterval:         "10s",
		ClaimTTL:             "30m",
		HeartbeatInterval:    "1m",
		ShutdownTimeout:      "5m",
		IdleTimeout:          "10m",
	}
}

// Path returns the daemon configuration path under root.
func Path(root string) string { return filepath.Join(root, Filename) }

// EnsureDefault creates daemon.yaml under root with the documented defaults
// when the file does not exist. It is the only function that mutates the
// daemon configuration file; read-only commands (status, stop) must not call
// it. Returns true when the file was created.
func EnsureDefault(root string) (bool, error) {
	if root == "" {
		return false, errors.New("daemonconfig: root is required")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return false, fmt.Errorf("create daemon root: %w", err)
	}
	path := Path(root)
	if _, err := os.Stat(path); err == nil {
		return false, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return false, fmt.Errorf("inspect %s: %w", path, err)
	}
	data, err := marshalDocumentedDefaults()
	if err != nil {
		return false, err
	}
	// O_CREATE|O_EXCL avoids racing a concurrent daemon start that may also
	// be creating the same file.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return false, nil
		}
		return false, fmt.Errorf("create %s: %w", path, err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return false, fmt.Errorf("write %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return false, fmt.Errorf("close %s: %w", path, err)
	}
	return true, nil
}

// Load reads daemon.yaml under root. The boolean reports whether the file was
// present. A missing file is not an error; the caller proceeds with built-in
// defaults.
func Load(root string) (File, bool, error) {
	if root == "" {
		return File{}, false, errors.New("daemonconfig: root is required")
	}
	path := Path(root)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return File{}, false, nil
		}
		return File{}, false, fmt.Errorf("read %s: %w", path, err)
	}
	var file File
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&file); err != nil {
		return File{}, true, fmt.Errorf("decode %s: %w", path, err)
	}
	if err := file.Validate(); err != nil {
		return File{}, true, fmt.Errorf("validate %s: %w", path, err)
	}
	return file, true, nil
}

// Validate checks that the fields present in the file are well-formed. An
// absent field is always valid because the resolver falls back to the next
// layer.
func (f File) Validate() error {
	if f.Supervisor != "" && f.Supervisor != "claude" && f.Supervisor != "codex" {
		return fmt.Errorf("supervisor must be one of: claude, codex (got %q)", f.Supervisor)
	}
	if f.MaxConcurrentTasks != nil && *f.MaxConcurrentTasks < 0 {
		return fmt.Errorf("max_concurrent_tasks must be >= 0 (got %d)", *f.MaxConcurrentTasks)
	}
	if f.MaxConcurrentPerRepo != nil && *f.MaxConcurrentPerRepo < 0 {
		return fmt.Errorf("max_concurrent_per_repo must be >= 0 (got %d)", *f.MaxConcurrentPerRepo)
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{"poll_interval", f.PollInterval},
		{"claim_ttl", f.ClaimTTL},
		{"heartbeat_interval", f.HeartbeatInterval},
		{"shutdown_timeout", f.ShutdownTimeout},
		{"idle_timeout", f.IdleTimeout},
	} {
		if field.value == "" {
			continue
		}
		if _, err := time.ParseDuration(field.value); err != nil {
			return fmt.Errorf("%s must be a valid Go duration (got %q): %w", field.name, field.value, err)
		}
	}
	return nil
}

// Duration parses a duration string field. An empty string returns ok=false so
// callers can leave the resolved value untouched.
func Duration(value string) (time.Duration, bool, error) {
	if value == "" {
		return 0, false, nil
	}
	d, err := time.ParseDuration(value)
	if err != nil {
		return 0, false, err
	}
	return d, true, nil
}

func marshalDocumentedDefaults() ([]byte, error) {
	defaults := Defaults()
	var buf bytes.Buffer
	buf.WriteString("# Galley daemon startup defaults.\n")
	buf.WriteString("# Edit these values to change daemon-wide defaults without re-specifying CLI flags.\n")
	buf.WriteString("# CLI flags on `galley daemon run` or `galley daemon start` override the matching field.\n")
	buf.WriteString("# Repository `environment.yaml` `supervisor.default_cli` overrides supervisor for that task only.\n")
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(defaults); err != nil {
		return nil, fmt.Errorf("encode defaults: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("close encoder: %w", err)
	}
	return buf.Bytes(), nil
}
