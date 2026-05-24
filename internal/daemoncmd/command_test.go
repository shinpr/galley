package daemoncmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shinpr/galley/internal/daemon"
	"github.com/shinpr/galley/internal/daemonconfig"
)

func TestForegroundArgsRemovesStartCommand(t *testing.T) {
	t.Parallel()
	got := foregroundArgs([]string{"--root", "workflow", "start", "--poll-interval", "1s"}, "start")
	want := []string{"--root", "workflow", "--poll-interval", "1s"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("args got %#v, want %#v", got, want)
	}
}

func TestStatusReportsNotRunning(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	cmd := NewCommand("daemon")
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--root", root, "status"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "galley daemon not running") {
		t.Fatalf("stdout got %q", stdout.String())
	}
}

func TestStatusJSONReportsRoot(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	cmd := NewCommand("daemon")
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--root", root, "status", "--output", "json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Running bool   `json:"running"`
		Root    string `json:"root"`
		PIDFile string `json:"pid_file"`
		LogFile string `json:"log_file"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Running || payload.Root != root || payload.PIDFile == "" || payload.LogFile == "" {
		t.Fatalf("payload got %#v", payload)
	}
}

func TestStatusRuntimeRestoresDaemonArgs(t *testing.T) {
	t.Parallel()
	runtime := statusRuntime{}.withArgv([]string{
		"/bin/galley",
		"daemon",
		"--supervisor",
		"codex",
		"--max-concurrent-tasks",
		"3",
		"--max-concurrent-per-repo=2",
	})
	if runtime.Supervisor != "codex" {
		t.Fatalf("supervisor got %q", runtime.Supervisor)
	}
	if runtime.MaxConcurrentTasks != 3 || runtime.MaxConcurrentPerRepo != 2 {
		t.Fatalf("parsed values got %#v", runtime)
	}
}

func TestStatusRuntimeDefaultsSupervisorForDaemonWithoutFlag(t *testing.T) {
	t.Parallel()
	runtime := statusRuntime{}.withArgv([]string{"/bin/galley", "daemon"})
	if runtime.Supervisor != daemon.DefaultSupervisor {
		t.Fatalf("supervisor got %q", runtime.Supervisor)
	}
}

func TestDaemonHelpReportsCodexSupervisorDefault(t *testing.T) {
	t.Parallel()
	cmd := NewCommand("daemon")
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "defaults to "+daemon.DefaultSupervisor) {
		t.Fatalf("help output missing codex supervisor default: %q", stdout.String())
	}
}

func TestStatusJSONOmitsSupervisorField(t *testing.T) {
	t.Parallel()
	// AC7 / D3: `galley daemon status --output json` must not include a
	// single `supervisor` field because per-repository environment.yaml
	// supervisor.default_cli can override the daemon startup supervisor
	// for individual tasks; a daemon-wide supervisor field would be
	// misleading. The resolved supervisor for a real task is persisted
	// in runs/<run-id>/supervisor.json (AC8).
	root := t.TempDir()
	cmd := NewCommand("daemon")
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--root", root, "status", "--output", "json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if _, present := raw["supervisor"]; present {
		t.Fatalf("status JSON must not include a supervisor field: %s", stdout.String())
	}
}

func TestRunOnceCreatesDaemonYAML(t *testing.T) {
	t.Parallel()
	// AC1: when `galley daemon run` is invoked and daemon.yaml does not
	// exist under the selected daemon root, the system creates it with
	// documented defaults before running the daemon. With --once and an
	// empty queue the daemon loop exits immediately and we can inspect
	// the side effect.
	root := t.TempDir()
	cmd := NewCommand("daemon")
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--root", root, "run", "--once"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, daemonconfig.Filename))
	if err != nil {
		t.Fatalf("daemon.yaml not created: %v", err)
	}
	for _, want := range []string{"supervisor: codex", "poll_interval: 10s", "shutdown_timeout: 5m"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("daemon.yaml missing %q\ncontent: %s", want, string(data))
		}
	}
}

func TestApplyDaemonConfigFillsAbsentFlagsAndPreservesExplicit(t *testing.T) {
	t.Parallel()
	// AC2: when daemon.yaml exists, startup loads its durable defaults
	// without requiring CLI flags.
	// AC3: when both daemon.yaml and CLI options define the same setting,
	// the CLI option overrides daemon.yaml for that run. This test exercises
	// shutdown-timeout explicitly because the previous implementation
	// missed it.
	one := 1
	cfg := daemonconfig.File{
		Supervisor:           "claude",
		MaxConcurrentTasks:   &one,
		MaxConcurrentPerRepo: &one,
		PollInterval:         "30s",
		ClaimTTL:             "1h",
		HeartbeatInterval:    "15s",
		ShutdownTimeout:      "2m",
		IdleTimeout:          "5m",
	}
	// Case 1: no flags explicit, daemon.yaml wins.
	opts := daemon.Options{
		ShutdownTimeout: 5 * time.Minute,
	}
	var poll time.Duration
	applyDaemonConfig(&opts, &poll, cfg)
	if opts.Supervisor != "claude" || opts.SupervisorSource != daemon.SupervisorSourceDaemonConfig {
		t.Fatalf("daemon.yaml supervisor not applied: %#v", opts)
	}
	if opts.ShutdownTimeout != 2*time.Minute {
		t.Fatalf("daemon.yaml shutdown_timeout not applied: %s", opts.ShutdownTimeout)
	}
	if opts.ClaimTTL != time.Hour || opts.HeartbeatInterval != 15*time.Second || opts.IdleTimeout != 5*time.Minute {
		t.Fatalf("daemon.yaml durations not applied: %#v", opts)
	}
	if poll != 30*time.Second {
		t.Fatalf("daemon.yaml poll_interval not applied: %s", poll)
	}

	// Case 2: CLI flags explicit, CLI wins for shutdown-timeout and supervisor.
	opts2 := daemon.Options{
		ShutdownTimeout:  90 * time.Second,
		Supervisor:       "codex",
		SupervisorSource: daemon.SupervisorSourceCLI,
		Explicit: daemon.ExplicitOptions{
			Supervisor:      true,
			ShutdownTimeout: true,
		},
	}
	var poll2 time.Duration
	applyDaemonConfig(&opts2, &poll2, cfg)
	if opts2.Supervisor != "codex" || opts2.SupervisorSource != daemon.SupervisorSourceCLI {
		t.Fatalf("CLI supervisor must win: %#v", opts2)
	}
	if opts2.ShutdownTimeout != 90*time.Second {
		t.Fatalf("CLI shutdown_timeout must win: %s", opts2.ShutdownTimeout)
	}
}

func TestStopMissingPIDReturnsNotRunning(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	cmd := NewCommand("daemon")
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--root", root, "--pid-file", filepath.Join(root, "missing.pid"), "stop"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "daemon is not running") {
		t.Fatalf("expected not running error, got %v", err)
	}
}
