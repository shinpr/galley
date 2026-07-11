package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shinpr/galley/internal/daemonconfig"
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

func TestPreflightDefaultsSupervisorToClaude(t *testing.T) {
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

func TestPreflightAcceptsEveryCanonicalSupervisor(t *testing.T) {
	// Bind Preflight to the single supervisor-CLI source: every canonical value
	// must be accepted here, so a new value cannot be valid in daemonconfig yet
	// rejected at daemon startup.
	for _, supervisor := range daemonconfig.SupervisorCLIs() {
		supervisor := supervisor
		t.Run(supervisor, func(t *testing.T) {
			t.Parallel()
			opts := Options{Root: t.TempDir(), Supervisor: supervisor}
			if supervisor == "glm" {
				opts.GLMAuthToken = "zai-token"
			}
			if _, err := Preflight(opts); err != nil {
				t.Fatalf("Preflight should accept canonical supervisor %q: %v", supervisor, err)
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

func TestEffectiveSupervisorHonorsGLMProfileOverride(t *testing.T) {
	t.Parallel()
	profiles := profile.Bundle{Environment: &profile.Environment{Supervisor: &profile.SupervisorDefault{DefaultCLI: "glm"}}}
	if got := effectiveOptionsForProfiles(Options{Supervisor: "codex"}, profiles).Supervisor; got != "glm" {
		t.Fatalf("profile supervisor override = %q, want glm", got)
	}
}

func TestPreflightRejectsGLMSupervisorWithoutToken(t *testing.T) {
	t.Parallel()
	_, err := Preflight(Options{Root: t.TempDir(), Supervisor: "glm"})
	if err == nil {
		t.Fatal("expected glm supervisor without token to fail fast")
	}
	if !strings.Contains(err.Error(), "glm_api_key") {
		t.Fatalf("error must name the missing config key, got %q", err)
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

func TestEffectiveOptionsForProfilesAppliesRepoSupervisorModel(t *testing.T) {
	t.Parallel()
	// AC1: a repository supervisor.model resolves onto the effective daemon
	// options unchanged so it can reach the supervisor adapter later. The model
	// resolves independently of default_cli: pinning a model must not require
	// also overriding the supervisor selection.
	opts := Options{
		Root:             t.TempDir(),
		Supervisor:       "codex",
		SupervisorSource: SupervisorSourceCLI,
	}
	effective := effectiveOptionsForProfiles(opts, profile.Bundle{
		Environment: &profile.Environment{
			Supervisor: &profile.SupervisorDefault{Model: "pinned-model-42"},
		},
	})
	if effective.SupervisorModel != "pinned-model-42" {
		t.Fatalf("supervisor model got %q, want pinned-model-42", effective.SupervisorModel)
	}
	// default_cli was not set in the profile, so supervisor selection is
	// preserved from the CLI layer.
	if effective.Supervisor != "codex" || effective.SupervisorSource != SupervisorSourceCLI {
		t.Fatalf("supervisor selection changed unexpectedly: %q/%q", effective.Supervisor, effective.SupervisorSource)
	}
}

func TestEffectiveOptionsForProfilesTreatsBlankSupervisorModelAsUnset(t *testing.T) {
	t.Parallel()
	// AC3: an absent or whitespace-only supervisor.model preserves the CLI
	// default (empty SupervisorModel), so the adapter omits --model.
	cases := map[string]*profile.SupervisorDefault{
		"absent":          {DefaultCLI: "claude"},
		"empty":           {Model: ""},
		"whitespace-only": {Model: "   "},
	}
	for name, sup := range cases {
		sup := sup
		t.Run(name, func(t *testing.T) {
			effective := effectiveOptionsForProfiles(Options{Root: t.TempDir()}, profile.Bundle{
				Environment: &profile.Environment{Supervisor: sup},
			})
			if effective.SupervisorModel != "" {
				t.Fatalf("%s: supervisor model got %q, want empty", name, effective.SupervisorModel)
			}
		})
	}
}

func TestWriteSupervisorEvidenceRecordsPinnedModel(t *testing.T) {
	t.Parallel()
	// AC4: a pinned model is observable in run evidence and reported as coming
	// from the environment profile rather than the CLI default.
	runDir := t.TempDir()
	if err := writeSupervisorEvidence(runDir, Options{
		Supervisor:       "codex",
		SupervisorSource: SupervisorSourceRepoProfile,
		SupervisorModel:  "pinned-model-42",
	}); err != nil {
		t.Fatal(err)
	}
	got := readSupervisorEvidence(t, runDir)
	if got["model"] != "pinned-model-42" {
		t.Fatalf("model got %q, want pinned-model-42", got["model"])
	}
	if got["model_source"] != SupervisorModelSourceProfile {
		t.Fatalf("model_source got %q, want %s", got["model_source"], SupervisorModelSourceProfile)
	}
}

func TestWriteSupervisorEvidenceRecordsCLIDefaultModel(t *testing.T) {
	t.Parallel()
	// AC4: an omitted model is not reported as pinned; the evidence records an
	// empty model and a cli_default source so a default is distinguishable from
	// a pin.
	runDir := t.TempDir()
	if err := writeSupervisorEvidence(runDir, Options{
		Supervisor:       "claude",
		SupervisorSource: SupervisorSourceDefault,
	}); err != nil {
		t.Fatal(err)
	}
	got := readSupervisorEvidence(t, runDir)
	if got["model"] != "" {
		t.Fatalf("model got %q, want empty", got["model"])
	}
	if got["model_source"] != SupervisorModelSourceCLIDefault {
		t.Fatalf("model_source got %q, want %s", got["model_source"], SupervisorModelSourceCLIDefault)
	}
}

func readSupervisorEvidence(t *testing.T, runDir string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(runDir, "supervisor.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	return got
}

func TestEffectiveOptionsForProfilesCleansWorktreesByDefault(t *testing.T) {
	t.Parallel()
	effective := effectiveOptionsForProfiles(Options{Root: t.TempDir()}, profile.Bundle{})
	if !effective.CleanupWorktrees {
		t.Fatalf("cleanup should default to true: %#v", effective)
	}
}

func TestEffectiveSupervisorPrecedenceCharacterization(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		base       Options
		repository string
		want       string
		wantSource string
	}{
		{name: "built-in default retained", base: Options{Supervisor: "claude", SupervisorSource: SupervisorSourceDefault}, want: "claude", wantSource: SupervisorSourceDefault},
		{name: "daemon config retained", base: Options{Supervisor: "codex", SupervisorSource: SupervisorSourceDaemonConfig}, want: "codex", wantSource: SupervisorSourceDaemonConfig},
		{name: "explicit CLI retained", base: Options{Supervisor: "codex", SupervisorSource: SupervisorSourceCLI}, want: "codex", wantSource: SupervisorSourceCLI},
		{name: "repository overrides CLI", base: Options{Supervisor: "codex", SupervisorSource: SupervisorSourceCLI}, repository: "glm", want: "glm", wantSource: SupervisorSourceRepoProfile},
	}
	for _, tt := range tests {
		current := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			bundle := profile.Bundle{Environment: &profile.Environment{}}
			if current.repository != "" {
				bundle.Environment.Supervisor = &profile.SupervisorDefault{DefaultCLI: current.repository}
			}
			got := effectiveOptionsForProfiles(current.base, bundle)
			if got.Supervisor != current.want || got.SupervisorSource != current.wantSource {
				t.Fatalf("resolved supervisor=%q source=%q; want %q/%q", got.Supervisor, got.SupervisorSource, current.want, current.wantSource)
			}
		})
	}
}
