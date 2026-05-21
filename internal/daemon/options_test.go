package daemon

import (
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
	if opts.Supervisor != "codex" {
		t.Fatalf("default supervisor got %q, want codex", opts.Supervisor)
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

func TestEffectiveOptionsForProfilesCleansWorktreesByDefault(t *testing.T) {
	t.Parallel()
	effective := effectiveOptionsForProfiles(Options{Root: t.TempDir()}, profile.Bundle{})
	if !effective.CleanupWorktrees {
		t.Fatalf("cleanup should default to true: %#v", effective)
	}
}
