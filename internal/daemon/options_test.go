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

func TestEffectiveOptionsForProfilesResolvesSupervisorModel(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		env       *profile.Environment
		wantModel string
	}{
		{
			name:      "pinned model without default_cli",
			env:       &profile.Environment{Supervisor: &profile.SupervisorDefault{Model: "provider-model-x"}},
			wantModel: "provider-model-x",
		},
		{
			name:      "pinned model preserved exactly",
			env:       &profile.Environment{Supervisor: &profile.SupervisorDefault{DefaultCLI: "codex", Model: " spaced model "}},
			wantModel: " spaced model ",
		},
		{
			name:      "empty model omitted",
			env:       &profile.Environment{Supervisor: &profile.SupervisorDefault{DefaultCLI: "claude", Model: ""}},
			wantModel: "",
		},
		{
			name:      "no supervisor block",
			env:       &profile.Environment{},
			wantModel: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			effective := effectiveOptionsForProfiles(Options{Root: t.TempDir()}, profile.Bundle{Environment: tc.env})
			if effective.SupervisorModel != tc.wantModel {
				t.Fatalf("supervisor model got %q, want %q", effective.SupervisorModel, tc.wantModel)
			}
		})
	}
}

func TestWriteSupervisorEvidenceRecordsModelState(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name            string
		model           string
		wantModel       string
		wantModelSource string
	}{
		{name: "pinned", model: "provider-model-x", wantModel: "provider-model-x", wantModelSource: SupervisorModelSourceRepoProfile},
		{name: "omitted", model: "", wantModel: "", wantModelSource: SupervisorModelSourceCLIDefault},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runDir := t.TempDir()
			if err := writeSupervisorEvidence(runDir, Options{
				Supervisor:       "claude",
				SupervisorSource: SupervisorSourceRepoProfile,
				SupervisorModel:  tc.model,
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
			if got["model"] != tc.wantModel {
				t.Fatalf("model got %q, want %q", got["model"], tc.wantModel)
			}
			if got["model_source"] != tc.wantModelSource {
				t.Fatalf("model_source got %q, want %q (evidence %#v)", got["model_source"], tc.wantModelSource, got)
			}
		})
	}
}

func TestEffectiveOptionsForProfilesResolvesSupervisorEffort(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		env        *profile.Environment
		wantEffort string
	}{
		{
			name:       "explicit effort from profile",
			env:        &profile.Environment{Supervisor: &profile.SupervisorDefault{DefaultCLI: "codex", Effort: "minimal"}},
			wantEffort: "minimal",
		},
		{
			name:       "empty effort preserves CLI default",
			env:        &profile.Environment{Supervisor: &profile.SupervisorDefault{DefaultCLI: "claude", Effort: ""}},
			wantEffort: "",
		},
		{
			name:       "no supervisor block",
			env:        &profile.Environment{},
			wantEffort: "",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			effective := effectiveOptionsForProfiles(Options{Root: t.TempDir()}, profile.Bundle{Environment: tc.env})
			if effective.SupervisorEffort != tc.wantEffort {
				t.Fatalf("supervisor effort got %q, want %q", effective.SupervisorEffort, tc.wantEffort)
			}
		})
	}
}

func TestWriteSupervisorEvidenceRecordsEffortState(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name             string
		effort           string
		wantEffort       string
		wantEffortSource string
	}{
		{name: "explicit", effort: "minimal", wantEffort: "minimal", wantEffortSource: SupervisorEffortSourceRepoProfile},
		{name: "omitted", effort: "", wantEffort: "", wantEffortSource: SupervisorEffortSourceCLIDefault},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			runDir := t.TempDir()
			if err := writeSupervisorEvidence(runDir, Options{
				Supervisor:       "codex",
				SupervisorSource: SupervisorSourceRepoProfile,
				SupervisorEffort: tc.effort,
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
			if got["effort"] != tc.wantEffort {
				t.Fatalf("effort got %q, want %q (evidence %#v)", got["effort"], tc.wantEffort, got)
			}
			if got["effort_source"] != tc.wantEffortSource {
				t.Fatalf("effort_source got %q, want %q (evidence %#v)", got["effort_source"], tc.wantEffortSource, got)
			}
		})
	}
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
