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
//  3. Built-in defaults (Claude supervisor and the other documented daemon
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
	"slices"
	"strings"
	"time"

	"github.com/shinpr/galley/internal/provider"

	"go.yaml.in/yaml/v3"
)

// supervisorCLIs is the single source the --supervisor flag, daemon.yaml
// validation, daemon Preflight, and profile validation all consult so the
// accepted set cannot drift between them. glm additionally needs a token,
// enforced at startup/per task rather than by this list.
var supervisorCLIs = provider.SupervisorIDs()

// SupervisorCLIs returns the accepted supervisor adapter values in stable order.
func SupervisorCLIs() []string { return slices.Clone(supervisorCLIs) }

// IsValidSupervisor reports whether value is an accepted supervisor adapter.
func IsValidSupervisor(value string) bool { return slices.Contains(supervisorCLIs, value) }

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
	// GLMAPIKey is the Z.ai token used when glm is the executor or supervisor.
	// Absent/empty is valid; a missing token fails fast only when glm is selected.
	GLMAPIKey string `yaml:"glm_api_key,omitempty"`
	// Notifications configures the opt-in, best-effort notification command
	// hook the daemon runs after a task reaches a terminal published status.
	// A nil pointer (absent block) disables notifications entirely; the
	// resolution chain has no CLI flag equivalent because the hook is operator
	// config, not a runtime-tunable knob.
	Notifications *NotificationConfig `yaml:"notifications,omitempty"`
}

// NotificationConfig is the daemon.yaml `notifications` block. It is operator
// configuration at the same trust level as `setup.commands` and
// `required_checks`: the command string is operator-owned and is never
// interpolated with task-derived content. Task-derived data crosses into the
// command only through stdin JSON and namespaced GALLEY_* environment
// variables.
type NotificationConfig struct {
	// Enabled gates the hook. Default false (opt-in). When true, Command must
	// be set; validation rejects enabled-without-command so a misconfiguration
	// fails fast at daemon startup instead of silently never notifying.
	Enabled bool `yaml:"enabled"`
	// On lists the terminal task statuses that trigger the hook. An empty list
	// resolves to the default [failed, needs_supervisor_review]. accepted and
	// pr_opened are valid opt-in statuses but are not on by default.
	On []string `yaml:"on,omitempty"`
	// Command is the operator-configured shell command. It is executed through
	// the same cross-platform shell resolution as required checks. Task content
	// is never concatenated into this string.
	Command string `yaml:"command,omitempty"`
}

// DefaultNotificationEvents is the resolved default for notifications.on when
// the operator enables notifications without naming explicit statuses.
func DefaultNotificationEvents() []string {
	return []string{"failed", "needs_supervisor_review"}
}

// validNotificationEvents is the set of terminal task statuses a notification
// hook may subscribe to. failed and needs_supervisor_review are the defaults;
// accepted and pr_opened are valid opt-in statuses.
var validNotificationEvents = map[string]bool{
	"failed":                  true,
	"needs_supervisor_review": true,
	"accepted":                true,
	"pr_opened":               true,
}

// ResolvedOn returns the effective notification status list, applying the
// documented default when the operator left notifications.on empty. It returns
// nil when the receiver is nil so callers can treat "no config" uniformly.
func (n *NotificationConfig) ResolvedOn() []string {
	if n == nil {
		return nil
	}
	if len(n.On) == 0 {
		return DefaultNotificationEvents()
	}
	return append([]string(nil), n.On...)
}

// Matches reports whether the hook is enabled and subscribes to status.
func (n *NotificationConfig) Matches(status string) bool {
	if n == nil || !n.Enabled || n.Command == "" {
		return false
	}
	for _, e := range n.ResolvedOn() {
		if e == status {
			return true
		}
	}
	return false
}

// Defaults returns the documented daemon startup defaults that are written to
// daemon.yaml on first creation. They mirror the built-in CLI default values
// so an operator can edit a single file instead of relying on flag defaults.
func Defaults() File {
	one := 1
	return File{
		Supervisor:           "claude",
		MaxConcurrentTasks:   &one,
		MaxConcurrentPerRepo: &one,
		PollInterval:         "10s",
		ClaimTTL:             "30m",
		HeartbeatInterval:    "1m",
		ShutdownTimeout:      "5m",
		IdleTimeout:          "10m",
		Notifications: &NotificationConfig{
			Enabled: false,
			On:      DefaultNotificationEvents(),
		},
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
	// Supervisor membership is validated against the single SupervisorCLIs
	// source so this check, the --supervisor flag, and daemon Preflight cannot
	// disagree. The glm-token requirement is enforced separately at daemon
	// startup and per task, keeping this low-level package decoupled from the
	// executor runner.
	if f.Supervisor != "" && !IsValidSupervisor(f.Supervisor) {
		return fmt.Errorf("supervisor must be one of: %s (got %q)", strings.Join(SupervisorCLIs(), ", "), f.Supervisor)
	}
	// max_concurrent_tasks must be >= 1: the daemon always needs at least
	// one worker to make progress, and silently accepting 0 here just gets
	// rewritten by daemon.Options.withDefaults to 1 — leaving operators
	// unsure whether their daemon.yaml value took effect. This contract
	// matches the CLI flag, which is documented and defaulted to 1.
	if f.MaxConcurrentTasks != nil && *f.MaxConcurrentTasks < 1 {
		return fmt.Errorf("max_concurrent_tasks must be >= 1 (got %d)", *f.MaxConcurrentTasks)
	}
	// max_concurrent_per_repo accepts 0 to disable the per-repo limit,
	// matching the CLI `--max-concurrent-per-repo=0` contract.
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
	if err := f.Notifications.validate(); err != nil {
		return err
	}
	return nil
}

// validate checks the notifications block. A nil pointer (absent block) is
// always valid: notifications are opt-in and default to disabled.
func (n *NotificationConfig) validate() error {
	if n == nil {
		return nil
	}
	for _, event := range n.On {
		if !validNotificationEvents[event] {
			return fmt.Errorf("notifications.on contains unknown event %q (valid events: failed, needs_supervisor_review, accepted, pr_opened)", event)
		}
	}
	if n.Enabled && n.Command == "" {
		return errors.New("notifications.enabled is true but notifications.command is empty; set a command or disable notifications")
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
	buf.WriteString("#\n")
	buf.WriteString("# notifications: opt-in, best-effort command hook run after a task reaches a\n")
	buf.WriteString("# terminal status. Set `enabled: true` and a `command` to receive alerts; the\n")
	buf.WriteString("# command receives task data on stdin (JSON) and via GALLEY_* env vars, never\n")
	buf.WriteString("# concatenated into the command string. See docs/operations.md and the sample\n")
	buf.WriteString("# scripts under docs/examples/notifications/. Example:\n")
	buf.WriteString("#   notifications:\n")
	buf.WriteString("#     enabled: true\n")
	buf.WriteString("#     on: [failed, needs_supervisor_review]\n")
	buf.WriteString("#     command: \"/path/to/docs/examples/notifications/notify-slack.sh\"\n")
	buf.WriteString("#\n")
	buf.WriteString("# glm_api_key: Z.ai token used when glm is the executor (executor.cli: glm)\n")
	buf.WriteString("# or the supervisor (--supervisor glm / supervisor.default_cli). Leave unset\n")
	buf.WriteString("# unless you use glm. Example:\n")
	buf.WriteString("#   glm_api_key: \"<your-z.ai-token>\"\n")
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
