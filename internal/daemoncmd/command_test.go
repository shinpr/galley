package daemoncmd

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shinpr/galley/internal/daemon"
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
