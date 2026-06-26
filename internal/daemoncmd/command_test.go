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

func TestDaemonHelpReportsClaudeSupervisorDefault(t *testing.T) {
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
		t.Fatalf("help output missing claude supervisor default: %q", stdout.String())
	}
}

func TestStatusJSONOmitsRuntimeDefaultFields(t *testing.T) {
	t.Parallel()
	// AC7 / D3: `galley daemon status --output json` must not include
	// daemon startup defaults that can be supplied by either daemon.yaml
	// or per-repository environment.yaml. The single `supervisor` field is
	// misleading because per-repository `supervisor.default_cli` overrides
	// it per task. `max_concurrent_tasks` and `max_concurrent_per_repo`
	// are misleading because `status` only sees PID argv and cannot read
	// daemon.yaml-supplied values. Resolution evidence for actual tasks is
	// persisted in runs/<run-id>/supervisor.json (AC8).
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
	for _, field := range []string{"supervisor", "max_concurrent_tasks", "max_concurrent_per_repo"} {
		if _, present := raw[field]; present {
			t.Fatalf("status JSON must not include %q: %s", field, stdout.String())
		}
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
	for _, want := range []string{"supervisor: claude", "poll_interval: 10s", "shutdown_timeout: 5m"} {
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
	// every daemon.yaml-backed startup option, including the integer
	// concurrency fields, so a future change that drops one or weakens the
	// CLI-wins contract is caught.
	four := 4
	two := 2
	cfg := daemonconfig.File{
		Supervisor:           "claude",
		MaxConcurrentTasks:   &four,
		MaxConcurrentPerRepo: &two,
		PollInterval:         "30s",
		ClaimTTL:             "1h",
		HeartbeatInterval:    "15s",
		ShutdownTimeout:      "2m",
		IdleTimeout:          "5m",
	}
	// Case 1: no flags explicit, daemon.yaml wins for every field.
	opts := daemon.Options{
		ShutdownTimeout: 5 * time.Minute,
	}
	var poll time.Duration
	if err := applyDaemonConfig(&opts, &poll, cfg); err != nil {
		t.Fatal(err)
	}
	if opts.Supervisor != "claude" || opts.SupervisorSource != daemon.SupervisorSourceDaemonConfig {
		t.Fatalf("daemon.yaml supervisor not applied: %#v", opts)
	}
	if opts.MaxConcurrentTasks != 4 || !opts.Explicit.MaxConcurrentTasks {
		t.Fatalf("daemon.yaml max_concurrent_tasks not applied or not marked explicit: %#v", opts)
	}
	if opts.MaxConcurrentPerRepo != 2 || !opts.Explicit.MaxConcurrentPerRepo {
		t.Fatalf("daemon.yaml max_concurrent_per_repo not applied or not marked explicit: %#v", opts)
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

	// Case 2: CLI flags explicit, CLI wins for every field daemon.yaml also provides.
	opts2 := daemon.Options{
		ShutdownTimeout:      90 * time.Second,
		Supervisor:           "codex",
		SupervisorSource:     daemon.SupervisorSourceCLI,
		MaxConcurrentTasks:   8,
		MaxConcurrentPerRepo: 3,
		Explicit: daemon.ExplicitOptions{
			Supervisor:           true,
			ShutdownTimeout:      true,
			MaxConcurrentTasks:   true,
			MaxConcurrentPerRepo: true,
		},
	}
	var poll2 time.Duration
	if err := applyDaemonConfig(&opts2, &poll2, cfg); err != nil {
		t.Fatal(err)
	}
	if opts2.Supervisor != "codex" || opts2.SupervisorSource != daemon.SupervisorSourceCLI {
		t.Fatalf("CLI supervisor must win: %#v", opts2)
	}
	if opts2.MaxConcurrentTasks != 8 || opts2.MaxConcurrentPerRepo != 3 {
		t.Fatalf("CLI concurrency must win: %#v", opts2)
	}
	if opts2.ShutdownTimeout != 90*time.Second {
		t.Fatalf("CLI shutdown_timeout must win: %s", opts2.ShutdownTimeout)
	}
}

func TestApplyDaemonConfigPreservesZeroPerRepoLimit(t *testing.T) {
	t.Parallel()
	// daemon.yaml `max_concurrent_per_repo: 0` means "disable the per-repo
	// limit", matching the CLI `--max-concurrent-per-repo=0` contract. The
	// 0 must survive past daemon.Options.withDefaults; if applyDaemonConfig
	// did not mark the option explicit, withDefaults would silently turn
	// the user-configured 0 back into 1.
	zero := 0
	cfg := daemonconfig.File{MaxConcurrentPerRepo: &zero}
	opts := daemon.Options{MaxConcurrentPerRepo: 1}
	var poll time.Duration
	if err := applyDaemonConfig(&opts, &poll, cfg); err != nil {
		t.Fatal(err)
	}
	if opts.MaxConcurrentPerRepo != 0 || !opts.Explicit.MaxConcurrentPerRepo {
		t.Fatalf("daemon.yaml 0 must be preserved and marked explicit: %#v", opts)
	}
	preflighted, err := daemon.Preflight(daemon.Options{
		Root:                 t.TempDir(),
		Supervisor:           "codex",
		MaxConcurrentPerRepo: 0,
		Explicit:             daemon.ExplicitOptions{MaxConcurrentPerRepo: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if preflighted.MaxConcurrentPerRepo != 0 {
		t.Fatalf("Preflight must preserve max_concurrent_per_repo=0; got %d", preflighted.MaxConcurrentPerRepo)
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
