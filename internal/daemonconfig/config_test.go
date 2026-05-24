package daemonconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureDefaultCreatesFileWithDocumentedDefaults(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	created, err := EnsureDefault(root)
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatalf("expected EnsureDefault to create daemon.yaml on first call")
	}
	data, err := os.ReadFile(filepath.Join(root, Filename))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	for _, want := range []string{
		"supervisor: codex",
		"max_concurrent_tasks: 1",
		"max_concurrent_per_repo: 1",
		"poll_interval: 10s",
		"claim_ttl: 30m",
		"heartbeat_interval: 1m",
		"shutdown_timeout: 5m",
		"idle_timeout: 10m",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("daemon.yaml missing %q\ncontent:\n%s", want, content)
		}
	}
}

func TestEnsureDefaultPreservesExistingFile(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, Filename)
	custom := "supervisor: claude\n"
	if err := os.WriteFile(path, []byte(custom), 0o600); err != nil {
		t.Fatal(err)
	}
	created, err := EnsureDefault(root)
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatalf("EnsureDefault should not recreate an existing file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != custom {
		t.Fatalf("existing daemon.yaml was modified: %q", string(data))
	}
}

func TestLoadReturnsAbsentWhenMissing(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	file, present, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if present {
		t.Fatalf("expected present=false on missing daemon.yaml")
	}
	if file != (File{}) {
		t.Fatalf("expected zero-value File when missing, got %#v", file)
	}
}

func TestLoadParsesAllFields(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	body := `supervisor: claude
max_concurrent_tasks: 4
max_concurrent_per_repo: 2
poll_interval: 30s
claim_ttl: 1h
heartbeat_interval: 15s
shutdown_timeout: 2m
idle_timeout: 5m
`
	if err := os.WriteFile(filepath.Join(root, Filename), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	file, present, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if !present {
		t.Fatalf("expected present=true")
	}
	if file.Supervisor != "claude" {
		t.Fatalf("supervisor got %q", file.Supervisor)
	}
	if file.MaxConcurrentTasks == nil || *file.MaxConcurrentTasks != 4 {
		t.Fatalf("max_concurrent_tasks got %#v", file.MaxConcurrentTasks)
	}
	if file.MaxConcurrentPerRepo == nil || *file.MaxConcurrentPerRepo != 2 {
		t.Fatalf("max_concurrent_per_repo got %#v", file.MaxConcurrentPerRepo)
	}
	if file.PollInterval != "30s" || file.ClaimTTL != "1h" || file.HeartbeatInterval != "15s" || file.ShutdownTimeout != "2m" || file.IdleTimeout != "5m" {
		t.Fatalf("durations got %#v", file)
	}
}

func TestLoadRejectsUnknownSupervisor(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, Filename), []byte("supervisor: opus\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Load(root); err == nil {
		t.Fatalf("expected error for unsupported supervisor")
	}
}

func TestLoadRejectsBadDuration(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, Filename), []byte("poll_interval: not-a-duration\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Load(root); err == nil {
		t.Fatalf("expected error for invalid duration")
	}
}
