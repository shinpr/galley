package daemoncmd

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/shinpr/galley/internal/daemonconfig"
)

func TestConfigInitCreatesDefaultsWithoutDaemon(t *testing.T) {
	t.Parallel()
	// AC1: config init writes the documented defaults, reports the resolved
	// path, and leaves queue state and daemon processes untouched.
	root := t.TempDir()
	queuedDir := filepath.Join(root, "tasks", "queued")
	if err := os.MkdirAll(queuedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sentinelPath := filepath.Join(queuedDir, "sentinel.yaml")
	sentinel := []byte("id: task-sentinel\nstatus: queued\n")
	if err := os.WriteFile(sentinelPath, sentinel, 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := NewCommand("daemon")
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--root", root, "config", "init"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}

	configPath := daemonconfig.Path(root)
	if !strings.Contains(stdout.String(), configPath) {
		t.Fatalf("stdout must report the resolved path %s; got %q", configPath, stdout.String())
	}
	cfg, present, err := daemonconfig.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if !present {
		t.Fatalf("daemon.yaml not created at %s", configPath)
	}
	if !reflect.DeepEqual(cfg, daemonconfig.Defaults()) {
		t.Fatalf("daemon.yaml must load as daemonconfig.Defaults(); got %#v", cfg)
	}
	if _, err := os.Stat(filepath.Join(root, "galley-daemon.pid")); !os.IsNotExist(err) {
		t.Fatalf("config init must not create a daemon PID file; stat err=%v", err)
	}
	got, err := os.ReadFile(sentinelPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, sentinel) {
		t.Fatalf("queued sentinel changed: got %q want %q", got, sentinel)
	}
}

func TestConfigInitPreservesExistingDaemonYAML(t *testing.T) {
	t.Parallel()
	// AC2: an existing, operator-edited daemon.yaml is preserved byte-for-byte
	// and the command reports the existing file instead of overwriting it.
	root := t.TempDir()
	configPath := daemonconfig.Path(root)
	custom := []byte("supervisor: codex\nmax_concurrent_tasks: 3\nkimi_api_key: \"secret-token\"\n")
	if err := os.WriteFile(configPath, custom, 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := NewCommand("daemon")
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--root", root, "config", "init"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, custom) {
		t.Fatalf("existing daemon.yaml must be preserved byte-for-byte; got %q want %q", got, custom)
	}
	if !strings.Contains(stdout.String(), "already exists") || !strings.Contains(stdout.String(), configPath) {
		t.Fatalf("stdout must identify the existing file; got %q", stdout.String())
	}
}
