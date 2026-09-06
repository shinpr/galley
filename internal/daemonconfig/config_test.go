package daemonconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shinpr/galley/internal/provider"
)

func TestSupervisorCLIsIsTheValidationSource(t *testing.T) {
	for _, v := range provider.SupervisorIDs() {
		if err := (File{Supervisor: v}).Validate(); err != nil {
			t.Fatalf("Validate(supervisor=%q): %v", v, err)
		}
	}
	if provider.IsSupervisor("bogus") {
		t.Fatal("off-enum value must be invalid")
	}
	if err := (File{Supervisor: "bogus"}).Validate(); err == nil {
		t.Fatal("off-enum supervisor must fail Validate")
	}
}

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
		"supervisor: claude",
		"max_concurrent_tasks: 1",
		"max_concurrent_per_repo: 1",
		"poll_interval: 10s",
		"claim_ttl: 30m",
		"shutdown_timeout: 5m",
		"idle_timeout: 10m",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("daemon.yaml missing %q\ncontent:\n%s", want, content)
		}
	}
	if strings.Contains(content, "heartbeat_interval") {
		t.Fatalf("daemon.yaml must not expose heartbeat_interval; cadence is derived from claim_ttl\ncontent:\n%s", content)
	}
}

// TestEnsureDefaultThenLoadRoundTripsDocumentedDefaults closes the generate->parse
// seam. TestEnsureDefaultCreatesFileWithDocumentedDefaults only asserts YAML
// substrings, so a formatting change that made the generated file unparseable
// (or that drifted a value away from Defaults()) would keep that test green. This
// test Loads the file EnsureDefault wrote and asserts the parsed File equals the
// documented defaults, proving the generated file round-trips through the real
// decoder and validator.
func TestEnsureDefaultThenLoadRoundTripsDocumentedDefaults(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	created, err := EnsureDefault(root)
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatalf("expected EnsureDefault to create %s on first call", Filename)
	}

	file, present, err := Load(root)
	if err != nil {
		t.Fatalf("generated %s did not Load: %v", Filename, err)
	}
	if !present {
		t.Fatalf("expected present=true after EnsureDefault")
	}

	if file.Supervisor != "claude" {
		t.Fatalf("supervisor got %q, want claude", file.Supervisor)
	}
	if file.MaxConcurrentTasks == nil || *file.MaxConcurrentTasks != 1 {
		t.Fatalf("max_concurrent_tasks got %#v, want 1", file.MaxConcurrentTasks)
	}
	if file.MaxConcurrentPerRepo == nil || *file.MaxConcurrentPerRepo != 1 {
		t.Fatalf("max_concurrent_per_repo got %#v, want 1", file.MaxConcurrentPerRepo)
	}
	if file.PollInterval != "10s" || file.ClaimTTL != "30m" ||
		file.ShutdownTimeout != "5m" || file.IdleTimeout != "10m" {
		t.Fatalf("duration defaults drifted: %#v", file)
	}
	if file.Notifications == nil {
		t.Fatalf("notifications block missing from generated defaults")
	}
	if file.Notifications.Enabled {
		t.Fatalf("notifications must default to disabled, got enabled=true")
	}
	wantOn := DefaultNotificationEvents()
	got := file.Notifications.On
	if len(got) != len(wantOn) {
		t.Fatalf("notifications.on got %v, want %v", got, wantOn)
	}
	for i := range wantOn {
		if got[i] != wantOn[i] {
			t.Fatalf("notifications.on[%d] got %q, want %q", i, got[i], wantOn[i])
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
shutdown_timeout: 2m
idle_timeout: 5m
glm_api_key: glm-token
kimi_api_key: kimi-token
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
	if file.GLMAPIKey != "glm-token" || file.KimiAPIKey != "kimi-token" {
		t.Fatalf("provider API keys did not load: %#v", file)
	}
	if file.PollInterval != "30s" || file.ClaimTTL != "1h" || file.ShutdownTimeout != "2m" || file.IdleTimeout != "5m" {
		t.Fatalf("durations got %#v", file)
	}
}

func TestLoadIgnoresUnknownFields(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	body := `supervisor: claude
claim_ttl: 1h
heartbeat_interval: 15s
future_setting: enabled
notifications:
  enabled: false
  future_delivery: webhook
`
	if err := os.WriteFile(filepath.Join(root, Filename), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	file, present, err := Load(root)
	if err != nil {
		t.Fatalf("unknown fields should be ignored, got %v", err)
	}
	if !present {
		t.Fatalf("expected present=true")
	}
	if file.ClaimTTL != "1h" {
		t.Fatalf("claim_ttl got %q, want 1h", file.ClaimTTL)
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

func TestLoadRejectsKnownFieldTypeMismatch(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, Filename), []byte("max_concurrent_tasks: many\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := Load(root)
	if err == nil || !strings.Contains(err.Error(), "cannot unmarshal") {
		t.Fatalf("expected known-field type error, got %v", err)
	}
}

func TestLoadRejectsZeroMaxConcurrentTasks(t *testing.T) {
	t.Parallel()
	// The daemon always needs >= 1 worker. Accepting 0 in daemon.yaml and
	// silently turning it into 1 inside daemon.Options.withDefaults makes
	// the configured value invisible to operators; reject it instead.
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, Filename), []byte("max_concurrent_tasks: 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Load(root); err == nil {
		t.Fatalf("expected error for max_concurrent_tasks: 0")
	}
}

func TestLoadAcceptsZeroMaxConcurrentPerRepo(t *testing.T) {
	t.Parallel()
	// CLI `--max-concurrent-per-repo=0` disables the per-repo limit; the
	// daemon.yaml field has the same public meaning.
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, Filename), []byte("max_concurrent_per_repo: 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, present, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if !present {
		t.Fatalf("expected present=true")
	}
	if file.MaxConcurrentPerRepo == nil || *file.MaxConcurrentPerRepo != 0 {
		t.Fatalf("max_concurrent_per_repo got %#v, want pointer to 0", file.MaxConcurrentPerRepo)
	}
}

func TestLoadRejectsNegativeMaxConcurrentPerRepo(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, Filename), []byte("max_concurrent_per_repo: -1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Load(root); err == nil {
		t.Fatalf("expected error for max_concurrent_per_repo: -1")
	}
}

func TestNotificationsDefaultsResolveToDisabledWithDefaultEvents(t *testing.T) {
	t.Parallel()
	// Absent block: disabled, no matching.
	var absent *NotificationConfig
	if absent.Matches("failed") {
		t.Fatal("nil notifications must not match any status")
	}
	// Enabled with empty `on` resolves to the documented default events.
	cfg := &NotificationConfig{Enabled: true, Command: "x"}
	if got := cfg.ResolvedOn(); len(got) != 2 || got[0] != "failed" || got[1] != "needs_supervisor_review" {
		t.Fatalf("default events = %v, want [failed needs_supervisor_review]", got)
	}
	if !cfg.Matches("failed") || !cfg.Matches("needs_supervisor_review") {
		t.Fatal("default events must match failed and needs_supervisor_review")
	}
	if cfg.Matches("accepted") || cfg.Matches("pr_opened") {
		t.Fatal("accepted and pr_opened must be opt-in, not default")
	}
}

func TestNotificationsOptInStatuses(t *testing.T) {
	t.Parallel()
	cfg := &NotificationConfig{Enabled: true, Command: "x", On: []string{"accepted", "pr_opened"}}
	if !cfg.Matches("accepted") || !cfg.Matches("pr_opened") {
		t.Fatal("explicit opt-in statuses must match")
	}
	if cfg.Matches("failed") {
		t.Fatal("explicit `on` must not implicitly include defaults")
	}
	// Disabled never matches even with a command and events.
	cfg.Enabled = false
	if cfg.Matches("accepted") {
		t.Fatal("disabled notifications must not match")
	}
}

func TestLoadRejectsUnknownNotificationEvent(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	body := "notifications:\n  enabled: false\n  on:\n    - bogus_status\n"
	if err := os.WriteFile(filepath.Join(root, Filename), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := Load(root)
	if err == nil || !strings.Contains(err.Error(), "unknown event") {
		t.Fatalf("expected unknown event rejection, got %v", err)
	}
}

func TestLoadRejectsEnabledWithoutCommand(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	body := "notifications:\n  enabled: true\n"
	if err := os.WriteFile(filepath.Join(root, Filename), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := Load(root)
	if err == nil || !strings.Contains(err.Error(), "command is empty") {
		t.Fatalf("expected enabled-without-command rejection, got %v", err)
	}
}

func TestLoadParsesNotificationsBlock(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	body := "notifications:\n  enabled: true\n  on: [failed, accepted]\n  command: \"/bin/notify\"\n"
	if err := os.WriteFile(filepath.Join(root, Filename), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	file, present, err := Load(root)
	if err != nil || !present {
		t.Fatalf("load failed: present=%v err=%v", present, err)
	}
	n := file.Notifications
	if n == nil || !n.Enabled || n.Command != "/bin/notify" {
		t.Fatalf("notifications parsed wrong: %#v", n)
	}
	if len(n.On) != 2 || n.On[0] != "failed" || n.On[1] != "accepted" {
		t.Fatalf("notifications.on parsed wrong: %v", n.On)
	}
}

func TestEnsureDefaultDocumentsNotificationsDisabled(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if _, err := EnsureDefault(root); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, Filename))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	for _, want := range []string{"notifications:", "enabled: false"} {
		if !strings.Contains(content, want) {
			t.Fatalf("default daemon.yaml missing %q\n%s", want, content)
		}
	}
	// The generated default must itself be valid (round-trips through Validate).
	if _, _, err := Load(root); err != nil {
		t.Fatalf("generated default daemon.yaml does not validate: %v", err)
	}
}
