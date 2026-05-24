package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shinpr/galley/internal/profile"
)

func TestWithDefaultsAppliesIdleTimeout(t *testing.T) {
	t.Parallel()
	if got := (Options{}).withDefaults().IdleTimeout; got != 10*time.Minute {
		t.Fatalf("default idle timeout got %s, want 10m", got)
	}
	if got := (Options{IdleTimeout: 90 * time.Second}).withDefaults().IdleTimeout; got != 90*time.Second {
		t.Fatalf("explicit idle timeout overridden: got %s", got)
	}
}

func TestPreflightDefaultsSupervisorToCodex(t *testing.T) {
	t.Parallel()
	opts, err := Preflight(Options{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if opts.Supervisor != DefaultSupervisor {
		t.Fatalf("default supervisor got %q, want %s", opts.Supervisor, DefaultSupervisor)
	}
}

func TestPreflightHonorsExplicitSupervisors(t *testing.T) {
	t.Parallel()
	for _, supervisor := range []string{"claude", "codex"} {
		supervisor := supervisor
		t.Run(supervisor, func(t *testing.T) {
			t.Parallel()
			opts, err := Preflight(Options{Root: t.TempDir(), Supervisor: supervisor})
			if err != nil {
				t.Fatal(err)
			}
			if opts.Supervisor != supervisor {
				t.Fatalf("supervisor got %q, want %q", opts.Supervisor, supervisor)
			}
		})
	}
}

func TestPreflightRejectsUnsupportedSupervisor(t *testing.T) {
	t.Parallel()
	if _, err := Preflight(Options{Root: t.TempDir(), Supervisor: "opus"}); err == nil {
		t.Fatal("expected unsupported supervisor error")
	}
}

func TestEffectiveOptionsForProfilesUsesEnvironmentOperations(t *testing.T) {
	t.Parallel()
	cleanup := false
	opts := Options{Root: t.TempDir(), Supervisor: "codex"}
	effective := effectiveOptionsForProfiles(opts, profile.Bundle{
		Environment: &profile.Environment{
			PR: profile.PRSettings{
				Enabled: true,
				Base:    "develop",
				Comments: profile.PRCommentSettings{
					Enabled: true,
					Reply:   true,
				},
			},
			Worktree: profile.WorktreeSettings{Cleanup: &cleanup},
		},
	})
	if !effective.OpenPR || !effective.CommitOnAccept || effective.PRBase != "develop" {
		t.Fatalf("PR options got %#v", effective)
	}
	if !effective.PollPRComments || !effective.ReplyPRComments {
		t.Fatalf("comment options got %#v", effective)
	}
	if effective.CleanupWorktrees {
		t.Fatalf("cleanup should follow environment profile: %#v", effective)
	}
}

func TestEffectiveOptionsForProfilesAppliesRepoSupervisor(t *testing.T) {
	t.Parallel()
	// AC4 / D1: when environment.yaml supervisor.default_cli is set, the
	// daemon must use that supervisor for the task and record the source
	// as the repository environment profile so AC8 run evidence reflects
	// the precedence chain.
	opts := Options{
		Root:             t.TempDir(),
		Supervisor:       "codex",
		SupervisorSource: SupervisorSourceCLI,
	}
	effective := effectiveOptionsForProfiles(opts, profile.Bundle{
		Environment: &profile.Environment{
			Supervisor: &profile.SupervisorDefault{DefaultCLI: "claude"},
		},
	})
	if effective.Supervisor != "claude" {
		t.Fatalf("supervisor got %q, want claude", effective.Supervisor)
	}
	if effective.SupervisorSource != SupervisorSourceRepoProfile {
		t.Fatalf("source got %q, want %s", effective.SupervisorSource, SupervisorSourceRepoProfile)
	}
}

func TestEffectiveOptionsForProfilesKeepsCLISupervisorWhenRepoEmpty(t *testing.T) {
	t.Parallel()
	// AC5: when no repository supervisor is configured for a task, the
	// daemon retains the resolution from CLI/daemon.yaml/default.
	opts := Options{
		Root:             t.TempDir(),
		Supervisor:       "claude",
		SupervisorSource: SupervisorSourceDaemonConfig,
	}
	effective := effectiveOptionsForProfiles(opts, profile.Bundle{
		Environment: &profile.Environment{},
	})
	if effective.Supervisor != "claude" || effective.SupervisorSource != SupervisorSourceDaemonConfig {
		t.Fatalf("unexpected effective opts: supervisor=%q source=%q", effective.Supervisor, effective.SupervisorSource)
	}
}

func TestWriteSupervisorEvidenceRecordsResolvedAndSource(t *testing.T) {
	t.Parallel()
	// AC8: each daemon-processed task persists the resolved supervisor and
	// the source that determined it so reviewers can map the precedence
	// chain (environment_profile / cli / daemon_config / default) without
	// re-deriving it from daemon logs.
	runDir := t.TempDir()
	if err := writeSupervisorEvidence(runDir, Options{
		Supervisor:       "claude",
		SupervisorSource: SupervisorSourceRepoProfile,
	}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(runDir, "supervisor.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got["resolved"] != "claude" || got["source"] != SupervisorSourceRepoProfile {
		t.Fatalf("supervisor evidence got %#v", got)
	}
}

func TestEffectiveOptionsForProfilesCleansWorktreesByDefault(t *testing.T) {
	t.Parallel()
	effective := effectiveOptionsForProfiles(Options{Root: t.TempDir()}, profile.Bundle{})
	if !effective.CleanupWorktrees {
		t.Fatalf("cleanup should default to true: %#v", effective)
	}
}
