package setup

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shinpr/galley/internal/profile"
	"github.com/shinpr/galley/internal/task"
)

func setupTask() task.Task {
	return task.Task{
		ID:                 "setup-test",
		AcceptanceCriteria: []task.AcceptanceCriterion{{ID: "AC1", Text: "AC1", Verification: "see AC1", Status: "pending"}},
		Scope:              task.Scope{AllowedPaths: []string{"."}},
		Executor:           task.Executor{CLI: "claude"},
	}
}

func writeSetupEnvironmentProfile(t *testing.T, dir string, body string) string {
	t.Helper()
	path := filepath.Join(dir, "environment-local.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// workdirQuote returns a YAML double-quoted scalar for a path.
func workdirQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// TestSetupPreflightStaleSetupPlanCanBeRepairedBySetupExecutor proves a stale setup
// plan reaches the setup executor, whose repaired plan replaces it in the update.
func TestSetupPreflightStaleSetupPlanCanBeRepairedBySetupExecutor(t *testing.T) {
	work := t.TempDir()
	runDir := t.TempDir()
	dir := t.TempDir()
	envPath := writeSetupEnvironmentProfile(t, dir, `id: "stale"
cwd: `+workdirQuote(work)+`
commands:
  test_unit: "true"
constraints:
  network: "approval_required"
  secrets_policy: "never_read_env_files"
  destructive_commands: "deny"
setup:
  commands:
    - run: "false"
      why: "stale authored command that no longer works"
`)
	env, err := profile.LoadEnvironment(envPath)
	if err != nil {
		t.Fatal(err)
	}
	res, update, err := Run(context.Background(), Options{
		Task:                   setupTask(),
		WorkDir:                work,
		RunDir:                 runDir,
		Profiles:               profile.Bundle{Environment: &env},
		EnvironmentProfilePath: envPath,
		ExecutorRunner: func(_ context.Context, opts Options) (*Result, error) {
			if opts.Profiles.Environment == nil || opts.Profiles.Environment.Setup == nil {
				t.Fatalf("setup executor did not receive stale setup plan: %+v", opts.Profiles.Environment)
			}
			return &Result{
				Status:             StatusReady,
				Commands:           []CommandAttempt{{Run: "false", Why: "stale authored command that no longer works", Source: SourceEnvironmentSetup, ExitCode: 1}, {Run: "echo discovered", Source: SourceDiscovered, ExitCode: 0}},
				SuccessfulCommands: []profile.SetupCommand{{Run: "echo discovered", Why: "actual working command"}},
				ReadinessEvidence:  "discovered plan passed",
				Source:             SourceDiscovered,
				Provider:           "claude",
			}, nil
		},
	})
	if err != nil {
		t.Fatalf("setup preflight: %v", err)
	}
	if res == nil || res.Status != StatusReady {
		t.Fatalf("result: %+v", res)
	}
	if update == nil || !update.Changed || update.Before == nil || len(update.Before.Commands) == 0 || update.Before.Commands[0].Run != "false" {
		t.Fatalf("environment update should record prior stale plan: %+v", update)
	}
}

// TestSetupPreflightAtomicProfileRewriteAndSecondRunSeededReuse proves a learned
// plan rewrites environment.yaml once, and a second run reuses it as the seed plan.
func TestSetupPreflightAtomicProfileRewriteAndSecondRunSeededReuse(t *testing.T) {
	work := t.TempDir()
	dir := t.TempDir()
	// The unquoted pr.base scalar proves the rewrite preserves YAML node style.
	envBody := `id: "absent-setup"
cwd: ` + workdirQuote(work) + `
commands:
  test_unit: "go test ./..."
constraints:
  network: "approval_required"
  secrets_policy: "never_read_env_files"
  destructive_commands: "deny"
pr:
  enabled: true
  base: main
`
	envPath := writeSetupEnvironmentProfile(t, dir, envBody)
	env1, err := profile.LoadEnvironment(envPath)
	if err != nil {
		t.Fatal(err)
	}
	setupCalls := 0
	learned := func(_ context.Context, opts Options) (*Result, error) {
		setupCalls++
		return learnedSetupResult(opts), nil
	}
	res1, update1, err := Run(context.Background(), Options{
		Task:                   setupTask(),
		WorkDir:                work,
		RunDir:                 t.TempDir(),
		Profiles:               profile.Bundle{Environment: &env1},
		EnvironmentProfilePath: envPath,
		ExecutorRunner:         learned,
	})
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if setupCalls != 1 {
		t.Fatalf("setupCalls first run got %d, want 1", setupCalls)
	}
	if res1 == nil || res1.Status != StatusReady {
		t.Fatalf("first run result: %+v", res1)
	}
	if update1 == nil || !update1.Changed {
		t.Fatalf("first run should have persisted plan: %+v", update1)
	}
	if !strings.Contains(update1.Diff, "+ run: \"echo learned\"") {
		t.Fatalf("environment update missing setup diff: %+v", update1)
	}

	env2 := assertProfileRewrittenWithLearnedSetup(t, envPath)

	res2, update2, err := Run(context.Background(), Options{
		Task:                   setupTask(),
		WorkDir:                work,
		RunDir:                 t.TempDir(),
		Profiles:               profile.Bundle{Environment: &env2},
		EnvironmentProfilePath: envPath,
		ExecutorRunner:         learned,
	})
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if setupCalls != 2 {
		t.Fatalf("setup executor call count got %d, want 2", setupCalls)
	}
	if res2 == nil || res2.Status != StatusReady {
		t.Fatalf("second run result: %+v", res2)
	}
	if update2 != nil {
		t.Fatalf("second run should not have rewritten environment.yaml: %+v", update2)
	}
	for _, c := range res2.Commands {
		if c.Source == SourceDiscovered {
			t.Fatalf("second run recorded discovered command but should use persisted seed plan: %+v", res2.Commands)
		}
	}
}

// learnedSetupResult mimics a setup executor that succeeds, reporting the
// seeded source once environment.setup is present.
func learnedSetupResult(opts Options) *Result {
	source := SourceDiscovered
	if opts.Profiles.Environment != nil && opts.Profiles.Environment.Setup != nil {
		source = SourceEnvironmentSetup
	}
	return &Result{
		Status:             StatusReady,
		Commands:           []CommandAttempt{{Run: "echo learned", Source: source, ExitCode: 0}},
		SuccessfulCommands: []profile.SetupCommand{{Run: "echo learned", Why: "learned setup"}},
		ReadinessEvidence:  "setup executor passed",
		Source:             source,
		Provider:           "claude",
	}
}

// assertProfileRewrittenWithLearnedSetup pins that the atomic rewrite persisted
// the learned setup, preserved unrelated content, and still validates.
func assertProfileRewrittenWithLearnedSetup(t *testing.T, envPath string) profile.Environment {
	t.Helper()
	rewritten, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	body := string(rewritten)
	if !strings.Contains(body, "echo learned") {
		t.Fatalf("setup not persisted: %s", body)
	}
	if !strings.Contains(body, "test_unit") || !strings.Contains(body, "approval_required") || !strings.Contains(body, "base: main") {
		t.Fatalf("unrelated content lost in rewrite: %s", body)
	}
	env, err := profile.LoadEnvironment(envPath)
	if err != nil {
		t.Fatalf("reload rewritten env: %v", err)
	}
	if vr := profile.ValidateEnvironment(env); !vr.Valid() {
		t.Fatalf("rewritten env failed validation: %v", vr.Errors)
	}
	if env.Setup == nil || len(env.Setup.Commands) != 1 || env.Setup.Commands[0].Run != "echo learned" {
		t.Fatalf("rewritten env setup wrong: %+v", env.Setup)
	}
	return env
}

// TestSetupPreflightReadyWithoutSuccessfulCommandsFailsAndKeepsEnvironmentUnchanged
// pins that ready without successful_commands fails diagnostically and leaves environment.yaml untouched.
func TestSetupPreflightReadyWithoutSuccessfulCommandsFailsAndKeepsEnvironmentUnchanged(t *testing.T) {
	work := t.TempDir()
	runDir := t.TempDir()
	dir := t.TempDir()
	// No setup field: this is the path where a learned plan would be persisted.
	envBody := `id: "no-learned-plan"
cwd: ` + workdirQuote(work) + `
commands:
  test_unit: "go test ./..."
constraints:
  network: "approval_required"
  secrets_policy: "never_read_env_files"
  destructive_commands: "deny"
pr:
  enabled: true
  base: "main"
`
	envPath := writeSetupEnvironmentProfile(t, dir, envBody)
	before, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	env, err := profile.LoadEnvironment(envPath)
	if err != nil {
		t.Fatal(err)
	}
	res, update, err := Run(context.Background(), Options{
		Task:                   setupTask(),
		WorkDir:                work,
		RunDir:                 runDir,
		Profiles:               profile.Bundle{Environment: &env},
		EnvironmentProfilePath: envPath,
		ExecutorRunner: func(_ context.Context, _ Options) (*Result, error) {
			return &Result{
				Status:            StatusReady,
				Commands:          []CommandAttempt{{Run: "echo ok", Source: SourceDiscovered, ExitCode: 0}},
				ReadinessEvidence: "executor claimed ready without reporting commands",
				Source:            SourceDiscovered,
				Provider:          "claude",
			}, nil
		},
	})
	if err == nil {
		t.Fatalf("expected setup preflight error; got res=%+v update=%+v", res, update)
	}
	if update != nil {
		t.Fatalf("update should be nil when no learned plan can be persisted: %+v", update)
	}
	after, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("environment.yaml was modified despite missing successful_commands:\nbefore=%s\nafter=%s", before, after)
	}
	data, readErr := os.ReadFile(filepath.Join(runDir, "setup_result.json"))
	if readErr != nil {
		t.Fatalf("setup_result.json missing: %v", readErr)
	}
	var saved Result
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatalf("decode setup_result.json: %v", err)
	}
	if saved.Status != StatusFailed {
		t.Fatalf("saved status got %q, want failed", saved.Status)
	}
	if saved.Error == "" || !strings.Contains(saved.Error, "successful_commands") {
		t.Fatalf("saved error must name the contract violation: %q", saved.Error)
	}
	if saved.RepairGuidance == "" {
		t.Fatalf("saved failure missing repair guidance: %+v", saved)
	}
}
