package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	setuppreflight "github.com/shinpr/galley/internal/preflight/setup"
	"github.com/shinpr/galley/internal/profile"
	"github.com/shinpr/galley/internal/runartifact"
	"github.com/shinpr/galley/internal/runner"
	"github.com/shinpr/galley/internal/task"
)

// setupTask returns a minimal task usable by setup preflight tests. Callers
// override fields they care about.
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

func runSetupPreflight(ctx context.Context, opts setuppreflight.Options) (*setuppreflight.Result, *setuppreflight.EnvironmentUpdate, error) {
	opts.ExecutorRunner = testSetupExecutorRunner
	return setuppreflight.Run(ctx, opts)
}

// TestSetupPreflightSequencesBeforeSkeletonAndExecutor proves AC2: the
// setup preflight runs after worktree/input-file preparation and before any
// acceptance skeleton work or implementation executor attempt. The setup
// executor runner stub records its observed order vs the daemon-level
// acceptance_skeleton_creator subprocess.
func TestSetupPreflightSequencesBeforeSkeletonAndExecutor(t *testing.T) {
	// Setup executor writes a sentinel file BEFORE skeleton creation.
	// AcceptanceSkeletonPreflight is invoked by processClaimedTask AFTER the
	// setup preflight, so if setup ran first the sentinel exists by the time
	// AcceptanceSkeletonPreflight's stub creator writes the skeleton file.
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	promptPath, schemaPath := writeDaemonPromptFiles(t)
	// Fake Claude that, when invoked as the implementation executor, fails
	// loudly if the sentinel from setup is missing. The skeleton creator path
	// is detected by the manifest substring in the system prompt; the setup
	// preflight stub does not spawn Claude.
	claudeBin := writeFakeClaude(t, `creator=0
for arg in "$@"; do
  case "$arg" in
    *"Galley Acceptance Skeleton Manifest"*) creator=1 ;;
  esac
done
if [ "$creator" = "1" ]; then
  if [ ! -f setup.sentinel ]; then
    echo "skeleton ran before setup" 1>&2
    exit 99
  fi
  mkdir -p internal/foo
  printf 'package foo_test\n' > internal/foo/foo_test.go
  printf '%s\n' '{"type":"result","result":"{\"outputs\":[{\"ac_id\":\"AC1\",\"path\":\"internal/foo/foo_test.go\",\"kind\":\"go-test\",\"purpose\":\"verify AC1\",\"satisfies\":\"AC1 observable behavior\",\"integration_point\":\"executor completes this skeleton before acceptance\",\"implementation_required\":true}],\"no_skeletons\":[]}"}'
  exit 0
fi
if [ ! -f setup.sentinel ]; then
  echo "executor ran before setup" 1>&2
  exit 98
fi
echo change > daemon-output.txt
echo '{"status":"completed","summary":"done","files_modified":["daemon-output.txt"],"acceptance_criteria":[{"id":"AC1","status":"satisfied","evidence":["diff"],"notes":"done"}],"verification":[],"scope_expansions":[],"decisions":[],"risks":[]}'
`)

	// Environment profile carries the prior setup plan as context, but the
	// daemon must delegate execution to the setup executor. The stub creates
	// setup.sentinel before AcceptanceSkeletonPreflight is invoked, which is
	// exactly the ordering AC2 requires.
	envDir := t.TempDir()
	envPath := writeSetupEnvironmentProfile(t, envDir, `id: "sequencing"
cwd: `+workdirQuote(repo)+`
commands:
  test_unit: "true"
constraints:
  network: "approval_required"
  secrets_policy: "never_read_env_files"
  destructive_commands: "deny"
setup:
  commands:
    - run: "touch setup.sentinel"
      why: "sentinel for setup-before-skeleton ordering proof"
`)
	withSetupExecutorRunner(t, func(_ context.Context, opts setuppreflight.Options) (*setuppreflight.Result, error) {
		if err := os.WriteFile(filepath.Join(opts.WorkDir, "setup.sentinel"), []byte("ok\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return &setuppreflight.Result{
			Status: setuppreflight.StatusReady,
			Commands: []setuppreflight.CommandAttempt{{
				Run:      "touch setup.sentinel",
				Why:      "sentinel for setup-before-skeleton ordering proof",
				Source:   setuppreflight.SourceEnvironmentSetup,
				ExitCode: 0,
			}},
			SuccessfulCommands: []profile.SetupCommand{{
				Run: "touch setup.sentinel",
				Why: "sentinel for setup-before-skeleton ordering proof",
			}},
			ReadinessEvidence: "setup executor ran the existing environment.setup plan before skeleton creation",
			Source:            setuppreflight.SourceEnvironmentSetup,
			Provider:          "claude",
		}, nil
	})

	taskPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, taskPath, repo)
	loaded, err := task.Load(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Preflight = &task.Preflight{AcceptanceSkeleton: &task.AcceptanceSkeletonConfig{Enabled: true}}
	if err := task.Save(taskPath, loaded); err != nil {
		t.Fatal(err)
	}

	if err := runTestDaemon(context.Background(), Options{
		Root:                   root,
		SystemPromptFile:       promptPath,
		JSONSchemaFile:         schemaPath,
		EnvironmentProfileFile: envPath,
		Once:                   true,
		MaxConcurrentTasks:     1,
		Supervisor:             "claude",
		ClaudeBin:              claudeBin,
	}); err != nil {
		t.Fatal(err)
	}

	donePath := filepath.Join(root, "tasks", "done", "task.yaml")
	if _, err := os.Stat(donePath); err != nil {
		t.Fatalf("task did not complete; setup likely did not run before skeleton/executor: %v", err)
	}
	doneTask, err := task.Load(donePath)
	if err != nil {
		t.Fatal(err)
	}
	// AC8: setup readiness must be recorded in task verification history.
	foundSetup := false
	for _, vc := range doneTask.Verification.Commands {
		if strings.HasPrefix(vc.Cmd, "<galley:setup") {
			foundSetup = true
			if vc.Status != "passed" {
				t.Fatalf("setup verification status got %q, want passed", vc.Status)
			}
			if !strings.Contains(vc.OutputExcerpt, "environment.yaml=unchanged") {
				t.Fatalf("unchanged-setup excerpt missing 'environment.yaml=unchanged': %q", vc.OutputExcerpt)
			}
			break
		}
	}
	if !foundSetup {
		t.Fatalf("task verification history missing <galley:setup> entry: %#v", doneTask.Verification.Commands)
	}
}

// TestSetupPreflightAbsentSetupInvokesExecutorWithEnvironmentAndSignals proves
// AC3: when environment.setup is absent the daemon invokes the setup executor
// with the full environment.commands map and repository setup signals as
// context.
func TestSetupPreflightAbsentSetupInvokesExecutorWithEnvironmentAndSignals(t *testing.T) {
	work := t.TempDir()
	runDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "package.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "go.mod"), []byte("module example.com/foo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := &profile.Environment{
		ID:       "absent-setup",
		CWD:      work,
		Commands: map[string]string{"test_unit": "go test ./...", "install": "npm ci"},
		Constraints: profile.Constraints{
			Network:             "approval_required",
			SecretsPolicy:       "never_read_env_files",
			DestructiveCommands: "deny",
		},
	}
	type captured struct {
		Commands map[string]string
		Signals  []string
		WorkDir  string
	}
	cap := &captured{}
	withSetupExecutorRunner(t, func(_ context.Context, opts setuppreflight.Options) (*setuppreflight.Result, error) {
		cap.Commands = map[string]string{}
		if opts.Profiles.Environment != nil {
			for k, v := range opts.Profiles.Environment.Commands {
				cap.Commands[k] = v
			}
		}
		signals := opts.RepositorySignals
		if signals == nil {
			signals = setuppreflight.DiscoverRepositorySignals(opts.WorkDir)
		}
		cap.Signals = append([]string{}, signals...)
		cap.WorkDir = opts.WorkDir
		return &setuppreflight.Result{
			Status:             setuppreflight.StatusReady,
			Commands:           []setuppreflight.CommandAttempt{{Run: "go mod download", Source: setuppreflight.SourceDiscovered, ExitCode: 0}},
			SuccessfulCommands: []profile.SetupCommand{{Run: "go mod download", Why: "fetch modules"}},
			ReadinessEvidence:  "stub readiness",
			Source:             setuppreflight.SourceDiscovered,
			Provider:           "claude",
		}, nil
	})
	res, _, err := runSetupPreflight(context.Background(), setuppreflight.Options{
		Task:     setupTask(),
		WorkDir:  work,
		RunDir:   runDir,
		Profiles: profile.Bundle{Environment: env},
	})
	if err != nil {
		t.Fatalf("setup preflight: %v", err)
	}
	if res == nil || res.Status != setuppreflight.StatusReady {
		t.Fatalf("result: %+v", res)
	}
	if cap.Commands["test_unit"] != "go test ./..." || cap.Commands["install"] != "npm ci" {
		t.Fatalf("environment.commands not passed to setup executor: %+v", cap.Commands)
	}
	foundPkg := false
	foundGoMod := false
	for _, s := range cap.Signals {
		if s == "package.json" {
			foundPkg = true
		}
		if s == "go.mod" {
			foundGoMod = true
		}
	}
	if !foundPkg || !foundGoMod {
		t.Fatalf("repository signals missing manifests: %+v", cap.Signals)
	}
	// setup_result.json is written for evidence routing (AC8).
	if _, err := os.Stat(filepath.Join(runDir, "setup_result.json")); err != nil {
		t.Fatalf("setup_result.json missing: %v", err)
	}
}

// TestSetupPreflightExistingSetupPlanDelegatesToSetupExecutor proves the daemon
// treats environment.setup.commands as setup executor input, not as commands the
// daemon runs directly. This preserves the setup executor's ability to observe
// command failures and repair the plan in the same model context.
func TestSetupPreflightExistingSetupPlanDelegatesToSetupExecutor(t *testing.T) {
	work := t.TempDir()
	runDir := t.TempDir()
	env := &profile.Environment{
		ID:  "delegated-setup",
		CWD: work,
		Setup: &profile.SetupPlan{Commands: []profile.SetupCommand{
			{Run: "touch setup.proof", Why: "seed plan for setup executor"},
		}},
		Constraints: profile.Constraints{
			Network:             "approval_required",
			SecretsPolicy:       "never_read_env_files",
			DestructiveCommands: "deny",
		},
	}
	called := false
	withSetupExecutorRunner(t, func(_ context.Context, opts setuppreflight.Options) (*setuppreflight.Result, error) {
		called = true
		if opts.Profiles.Environment == nil || opts.Profiles.Environment.Setup == nil {
			t.Fatalf("setup executor did not receive environment.setup: %+v", opts.Profiles.Environment)
		}
		if _, err := os.Stat(filepath.Join(work, "setup.proof")); !os.IsNotExist(err) {
			t.Fatalf("daemon executed environment.setup directly before setup executor; stat err=%v", err)
		}
		return &setuppreflight.Result{
			Status: setuppreflight.StatusReady,
			Commands: []setuppreflight.CommandAttempt{{
				Run:      "touch setup.proof",
				Why:      "seed plan for setup executor",
				Source:   setuppreflight.SourceEnvironmentSetup,
				ExitCode: 0,
			}},
			SuccessfulCommands: []profile.SetupCommand{{Run: "touch setup.proof", Why: "seed plan for setup executor"}},
			ReadinessEvidence:  "setup executor used the seeded plan",
			Source:             setuppreflight.SourceEnvironmentSetup,
			Provider:           "claude",
		}, nil
	})
	res, _, err := runSetupPreflight(context.Background(), setuppreflight.Options{
		Task:     setupTask(),
		WorkDir:  work,
		RunDir:   runDir,
		Profiles: profile.Bundle{Environment: env},
	})
	if err != nil {
		t.Fatalf("setup preflight: %v", err)
	}
	if res == nil || res.Status != setuppreflight.StatusReady {
		t.Fatalf("setup result: %+v", res)
	}
	if !called {
		t.Fatalf("setup executor runner was not invoked")
	}
}

// TestSetupExecutorCommandPlanClaudeAndCodex proves part of AC4: building the
// setup executor command plan for both Claude and Codex returns provider-shaped
// argv that embeds the bin path and references the canonical capture path each
// provider uses.
func TestSetupExecutorCommandPlanClaudeAndCodex(t *testing.T) {
	work := t.TempDir()
	runDir := t.TempDir()
	payload := []byte(`{"environment":{"id":"x"}}`)

	// Claude: command_plan must use ClaudeBin and prompt mode replace.
	claudeOpts := setuppreflight.Options{
		Task:      setupTask(),
		WorkDir:   work,
		RunDir:    runDir,
		Profiles:  profile.Bundle{},
		ClaudeBin: "/path/to/claude",
	}
	claudePlan, provider, err := setuppreflight.BuildExecutorCommandPlan(claudeOpts, payload)
	if err != nil {
		t.Fatalf("claude plan: %v", err)
	}
	if provider != "claude" {
		t.Fatalf("provider got %q", provider)
	}
	if len(claudePlan.Argv) == 0 || claudePlan.Argv[0] != "/path/to/claude" {
		t.Fatalf("claude argv[0] got %v", claudePlan.Argv)
	}

	// Codex: command_plan must use CodexBin and request --output-last-message
	// pointed at the attempt-scoped capture path used by parsing.
	codexTask := setupTask()
	codexTask.Executor.CLI = "codex"
	codexOpts := setuppreflight.Options{
		Task:     codexTask,
		WorkDir:  work,
		RunDir:   runDir,
		Profiles: profile.Bundle{},
		CodexBin: "/path/to/codex",
	}
	codexPlan, provider, err := setuppreflight.BuildExecutorCommandPlan(codexOpts, payload)
	if err != nil {
		t.Fatalf("codex plan: %v", err)
	}
	if provider != "codex" {
		t.Fatalf("provider got %q", provider)
	}
	if len(codexPlan.Argv) == 0 || codexPlan.Argv[0] != "/path/to/codex" {
		t.Fatalf("codex argv[0] got %v", codexPlan.Argv)
	}
	if len(claudePlan.EnvAppend) != 1 || claudePlan.EnvAppend[0] != "GALLEY_CLAUDE_GUARD_MODE=setup_executor" {
		t.Fatalf("claude setup executor guard env append got %v", claudePlan.EnvAppend)
	}
	if len(codexPlan.EnvAppend) != 0 {
		t.Fatalf("codex setup executor must not carry env append entries: %v", codexPlan.EnvAppend)
	}
	joined := strings.Join(codexPlan.Argv, " ")
	if !strings.Contains(joined, "--output-last-message") {
		t.Fatalf("codex argv missing --output-last-message: %v", codexPlan.Argv)
	}
	if !strings.Contains(joined, runner.CodexLastMessageFilename) {
		t.Fatalf("codex argv missing capture filename: %v", codexPlan.Argv)
	}

	grokTask := setupTask()
	grokTask.Executor.CLI = "grok"
	grokPlan, provider, err := setuppreflight.BuildExecutorCommandPlan(setuppreflight.Options{Task: grokTask, WorkDir: work, RunDir: t.TempDir(), GrokBin: "/path/to/grok"}, payload)
	if err != nil {
		t.Fatalf("grok plan: %v", err)
	}
	if provider != "grok" || grokPlan.Argv[0] != "/path/to/grok" {
		t.Fatalf("grok routing = %q %#v", provider, grokPlan.Argv)
	}
	grokArgs := strings.Join(grokPlan.Argv, " ")
	if !strings.Contains(grokArgs, "--prompt-file") || !strings.Contains(grokArgs, "--json-schema") || !strings.Contains(grokArgs, "--permission-mode bypassPermissions") || !strings.Contains(grokArgs, "--sandbox workspace") {
		t.Fatalf("grok setup argv = %#v", grokPlan.Argv)
	}
}

// TestSetupExecutorResolveResultClaudeAndCodex proves the second part of AC4:
// the result-parsing path accepts the same setuppreflight.Result JSON shape from both
// providers — Claude via stdout tail, Codex via the attempt-scoped
// --output-last-message file.
func TestSetupExecutorResolveResultClaudeAndCodex(t *testing.T) {
	runDir := t.TempDir()
	claudeJSON := `{"status":"ready","commands":[{"run":"go mod download","source":"discovered","exit_code":0}],"successful_commands":[{"run":"go mod download","why":"fetch modules"}],"readiness_evidence":"ok"}`
	// Claude path: parses arbitrary tail text that contains the JSON object.
	claudeOpts := setuppreflight.Options{Task: setupTask(), RunDir: runDir}
	claude, err := setuppreflight.ResolveExecutorResult(claudeOpts, "noise prefix\n"+claudeJSON+"\nnoise suffix")
	if err != nil {
		t.Fatalf("claude resolve: %v", err)
	}
	if claude.Status != setuppreflight.StatusReady || len(claude.Commands) != 1 || claude.Commands[0].Source != setuppreflight.SourceDiscovered {
		t.Fatalf("claude parsed result: %+v", claude)
	}

	// Codex setup keeps its raw message in the setup-specific artifact directory.
	codexJSON := `Some preamble text\n` + claudeJSON
	if err := os.MkdirAll(filepath.Join(runDir, runartifact.SetupCodexDirname), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, runartifact.SetupCodexDirname, runner.CodexLastMessageFilename), []byte(codexJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	codexTask := setupTask()
	codexTask.Executor.CLI = "codex"
	codexOpts := setuppreflight.Options{Task: codexTask, RunDir: runDir}
	codex, err := setuppreflight.ResolveExecutorResult(codexOpts, "")
	if err != nil {
		t.Fatalf("codex resolve: %v", err)
	}
	if codex.Status != setuppreflight.StatusReady || codex.SuccessfulCommands[0].Run != "go mod download" {
		t.Fatalf("codex parsed result: %+v", codex)
	}
}

// TestSetupPreflightStaleSetupPlanCanBeRepairedBySetupExecutor proves AC6: a
// stale setup plan is passed to the setup executor, which can record the failed
// seeded command and the successful replacement plan in one setup_result.json.
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
	withSetupExecutorRunner(t, func(_ context.Context, opts setuppreflight.Options) (*setuppreflight.Result, error) {
		if opts.Profiles.Environment == nil || opts.Profiles.Environment.Setup == nil {
			t.Fatalf("setup executor did not receive stale setup plan: %+v", opts.Profiles.Environment)
		}
		return &setuppreflight.Result{
			Status:             setuppreflight.StatusReady,
			Commands:           []setuppreflight.CommandAttempt{{Run: "false", Why: "stale authored command that no longer works", Source: setuppreflight.SourceEnvironmentSetup, ExitCode: 1}, {Run: "echo discovered", Source: setuppreflight.SourceDiscovered, ExitCode: 0}},
			SuccessfulCommands: []profile.SetupCommand{{Run: "echo discovered", Why: "actual working command"}},
			ReadinessEvidence:  "discovered plan passed",
			Source:             setuppreflight.SourceDiscovered,
			Provider:           "claude",
		}, nil
	})
	res, update, err := runSetupPreflight(context.Background(), setuppreflight.Options{
		Task:                   setupTask(),
		WorkDir:                work,
		RunDir:                 runDir,
		Profiles:               profile.Bundle{Environment: &env},
		EnvironmentProfilePath: envPath,
	})
	if err != nil {
		t.Fatalf("setup preflight: %v", err)
	}
	if res == nil || res.Status != setuppreflight.StatusReady {
		t.Fatalf("result: %+v", res)
	}
	// AC7: persistence-only check is in another test, but make sure update
	// metadata reflects that the prior plan differed.
	if update == nil || !update.Changed || update.Before == nil || len(update.Before.Commands) == 0 || update.Before.Commands[0].Run != "false" {
		t.Fatalf("environment update should record prior stale plan: %+v", update)
	}
}

// TestSetupPreflightAtomicProfileRewriteAndSecondRunSeededReuse proves AC7: a
// successful learned plan atomically rewrites environment.yaml setup while
// preserving unrelated content, and a second daemon run passes the persisted
// setup back to the setup executor as the seed plan without rewriting the
// profile again when the successful plan is unchanged.
func TestSetupPreflightAtomicProfileRewriteAndSecondRunSeededReuse(t *testing.T) {
	work := t.TempDir()
	dir := t.TempDir()
	// Profile without setup; includes unrelated content that must survive the
	// atomic rewrite. pr.base is intentionally written as an unquoted scalar
	// so the round-trip check below proves the YAML node style is preserved.
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
	runDir1 := t.TempDir()
	setupCalls := 0
	withSetupExecutorRunner(t, func(_ context.Context, opts setuppreflight.Options) (*setuppreflight.Result, error) {
		setupCalls++
		source := setuppreflight.SourceDiscovered
		if opts.Profiles.Environment != nil && opts.Profiles.Environment.Setup != nil {
			source = setuppreflight.SourceEnvironmentSetup
		}
		return &setuppreflight.Result{
			Status:             setuppreflight.StatusReady,
			Commands:           []setuppreflight.CommandAttempt{{Run: "echo learned", Source: source, ExitCode: 0}},
			SuccessfulCommands: []profile.SetupCommand{{Run: "echo learned", Why: "learned setup"}},
			ReadinessEvidence:  "setup executor passed",
			Source:             source,
			Provider:           "claude",
		}, nil
	})
	res1, update1, err := runSetupPreflight(context.Background(), setuppreflight.Options{
		Task:                   setupTask(),
		WorkDir:                work,
		RunDir:                 runDir1,
		Profiles:               profile.Bundle{Environment: &env1},
		EnvironmentProfilePath: envPath,
	})
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if setupCalls != 1 {
		t.Fatalf("setupCalls first run got %d, want 1", setupCalls)
	}
	if res1 == nil || res1.Status != setuppreflight.StatusReady {
		t.Fatalf("first run result: %+v", res1)
	}
	if update1 == nil || !update1.Changed {
		t.Fatalf("first run should have persisted plan: %+v", update1)
	}
	if !strings.Contains(update1.Diff, "+ run: \"echo learned\"") {
		t.Fatalf("environment update missing setup diff: %+v", update1)
	}

	// Verify the file was atomically rewritten with the new setup AND unrelated
	// content is preserved.
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
	// Re-validate the rewritten file end-to-end.
	env2, err := profile.LoadEnvironment(envPath)
	if err != nil {
		t.Fatalf("reload rewritten env: %v", err)
	}
	if vr := profile.ValidateEnvironment(env2); !vr.Valid() {
		t.Fatalf("rewritten env failed validation: %v", vr.Errors)
	}
	if env2.Setup == nil || len(env2.Setup.Commands) != 1 || env2.Setup.Commands[0].Run != "echo learned" {
		t.Fatalf("rewritten env setup wrong: %+v", env2.Setup)
	}

	// AC7 second-run reuse: re-running preflight with the persisted plan still
	// invokes the setup executor, but as a seeded run that reuses the saved
	// plan and records no new environment.yaml change.
	runDir2 := t.TempDir()
	res2, update2, err := runSetupPreflight(context.Background(), setuppreflight.Options{
		Task:                   setupTask(),
		WorkDir:                work,
		RunDir:                 runDir2,
		Profiles:               profile.Bundle{Environment: &env2},
		EnvironmentProfilePath: envPath,
	})
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if setupCalls != 2 {
		t.Fatalf("setup executor call count got %d, want 2", setupCalls)
	}
	if res2 == nil || res2.Status != setuppreflight.StatusReady {
		t.Fatalf("second run result: %+v", res2)
	}
	if update2 != nil {
		t.Fatalf("second run should not have rewritten environment.yaml: %+v", update2)
	}
	// All recorded commands should be from the seeded environment setup source.
	for _, c := range res2.Commands {
		if c.Source == setuppreflight.SourceDiscovered {
			t.Fatalf("second run recorded discovered command but should use persisted seed plan: %+v", res2.Commands)
		}
	}
}

// TestSetupPreflightSetupFailedClassifiesAndWritesEvidence proves AC9: when
// setup cannot make the worktree ready, the preflight returns an error and
// writes setup_result.json with status=failed, the executor's failure
// excerpts, and repair guidance.
func TestSetupPreflightSetupFailedClassifiesAndWritesEvidence(t *testing.T) {
	work := t.TempDir()
	runDir := t.TempDir()
	env := &profile.Environment{
		ID:  "fail",
		CWD: work,
		Constraints: profile.Constraints{
			Network:             "approval_required",
			SecretsPolicy:       "never_read_env_files",
			DestructiveCommands: "deny",
		},
	}
	withSetupExecutorRunner(t, func(_ context.Context, _ setuppreflight.Options) (*setuppreflight.Result, error) {
		return &setuppreflight.Result{
			Status:         setuppreflight.StatusFailed,
			Commands:       []setuppreflight.CommandAttempt{{Run: "npm ci", Source: setuppreflight.SourceDiscovered, ExitCode: 1, StderrExcerpt: "ENOENT"}},
			Error:          "setup executor could not install dependencies",
			RepairGuidance: "set environment.commands.install to a working command, or author environment.setup",
			Source:         setuppreflight.SourceDiscovered,
			Provider:       "claude",
		}, nil
	})
	_, _, err := runSetupPreflight(context.Background(), setuppreflight.Options{
		Task:     setupTask(),
		WorkDir:  work,
		RunDir:   runDir,
		Profiles: profile.Bundle{Environment: env},
	})
	if err == nil {
		t.Fatalf("expected setup preflight error")
	}
	// setup_result.json must contain the failure facts.
	data, readErr := os.ReadFile(filepath.Join(runDir, "setup_result.json"))
	if readErr != nil {
		t.Fatalf("setup_result.json missing: %v", readErr)
	}
	var saved setuppreflight.Result
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatalf("decode setup_result.json: %v", err)
	}
	if saved.Status != setuppreflight.StatusFailed {
		t.Fatalf("saved status got %q, want failed", saved.Status)
	}
	if saved.Error == "" || saved.RepairGuidance == "" {
		t.Fatalf("saved failure missing error or repair guidance: %+v", saved)
	}
	if len(saved.Commands) == 0 || saved.Commands[0].Run != "npm ci" {
		t.Fatalf("attempted commands not recorded: %+v", saved.Commands)
	}

	// Phase/kind classification: applying the failure path through the same
	// daemon helper used by processClaimedTask must yield phase=setup and
	// kind=setup_failed in the task latest error.
	tk := setupTask()
	appendFailureAttempt(&tk, setuppreflight.Phase, setuppreflight.FailedKind, err, runDir)
	if len(tk.Attempts) == 0 || tk.Attempts[len(tk.Attempts)-1].Error == nil {
		t.Fatalf("no attempt error appended")
	}
	last := tk.Attempts[len(tk.Attempts)-1].Error
	if last.Phase != setuppreflight.Phase || last.Kind != setuppreflight.FailedKind {
		t.Fatalf("phase/kind got phase=%q kind=%q want phase=%q kind=%q", last.Phase, last.Kind, setuppreflight.Phase, setuppreflight.FailedKind)
	}
}

// TestSetupPreflightSetupExecutorRunsBeforeSkeletonFilesExist proves AC10:
// setup readiness happens before acceptance skeleton creation, so the setup
// executor must not depend on task-specific skeleton files.
func TestSetupPreflightSetupExecutorRunsBeforeSkeletonFilesExist(t *testing.T) {
	work := t.TempDir()
	runDir := t.TempDir()
	env := &profile.Environment{
		ID:  "ac10",
		CWD: work,
		Setup: &profile.SetupPlan{Commands: []profile.SetupCommand{
			{Run: "true", Why: "noop"},
		}},
		Constraints: profile.Constraints{
			Network:             "approval_required",
			SecretsPolicy:       "never_read_env_files",
			DestructiveCommands: "deny",
		},
	}
	// Required quality check picks a repository readiness command (`true`),
	// NOT a skeleton-specific command. AC10's contract is that no
	// task-specific skeleton must exist for setup to declare ready.
	quality := &profile.Quality{
		ID: "test",
		RequiredChecks: []profile.RequiredCheck{
			{ID: "smoke", PreferredCommands: []string{"true"}, Required: true},
		},
		PassPolicy: profile.PassPolicy{MinScore: 1},
	}
	tk := setupTask()
	// The task carries an acceptance skeleton preflight config that would
	// normally drive AcceptanceSkeletonPreflight; the setup preflight must
	// finish first and must not depend on any skeleton file having been
	// created.
	tk.Preflight = &task.Preflight{AcceptanceSkeleton: &task.AcceptanceSkeletonConfig{Enabled: true}}
	withSetupExecutorRunner(t, func(_ context.Context, opts setuppreflight.Options) (*setuppreflight.Result, error) {
		if _, err := os.Stat(filepath.Join(opts.WorkDir, "internal/foo/foo_test.go")); !os.IsNotExist(err) {
			t.Fatalf("setup executor saw a skeleton file before skeleton preflight; stat err=%v", err)
		}
		return &setuppreflight.Result{
			Status: setuppreflight.StatusReady,
			Commands: []setuppreflight.CommandAttempt{{
				Run:      "true",
				Why:      "seeded setup command",
				Source:   setuppreflight.SourceEnvironmentSetup,
				ExitCode: 0,
			}},
			SuccessfulCommands: []profile.SetupCommand{{Run: "true", Why: "noop"}},
			ReadinessEvidence:  "setup executor confirmed repository readiness without skeleton files",
			Source:             setuppreflight.SourceEnvironmentSetup,
			Provider:           "claude",
		}, nil
	})
	res, _, err := runSetupPreflight(context.Background(), setuppreflight.Options{
		Task:     tk,
		WorkDir:  work,
		RunDir:   runDir,
		Profiles: profile.Bundle{Environment: env, Quality: quality},
	})
	if err != nil {
		t.Fatalf("setup preflight: %v", err)
	}
	if res == nil || res.Status != setuppreflight.StatusReady {
		t.Fatalf("setup result: %+v", res)
	}
	for _, c := range res.Commands {
		if strings.Contains(c.Run, "internal/foo/foo_test.go") {
			t.Fatalf("setup executor executed task-specific skeleton command: %+v", c)
		}
	}
	// The worktree directory must not contain any skeleton files written by
	// setup.
	entries, _ := os.ReadDir(work)
	if len(entries) != 0 {
		t.Fatalf("setup left files in worktree: %v", entries)
	}
}

// workdirQuote returns a YAML-safe double-quoted string for a path; t.TempDir
// returns absolute paths so simple Go strconv.Quote is sufficient for tests.
func workdirQuote(s string) string {
	// JSON-quote-style escaping is YAML 1.2 compatible for double-quoted
	// scalars in our examples.
	b, _ := json.Marshal(s)
	return string(b)
}

// TestSetupPreflightReadyWithoutSuccessfulCommandsFailsAndKeepsEnvironmentUnchanged
// pins the setup-result contract: a setup
// executor that returns status=ready with no successful_commands cannot
// produce a learned plan, so the daemon must downgrade the result to failed
// (with repair guidance), keep setup_result.json diagnostic, and NOT silently
// leave environment.yaml unchanged behind a fake-passing setup.
func TestSetupPreflightReadyWithoutSuccessfulCommandsFailsAndKeepsEnvironmentUnchanged(t *testing.T) {
	work := t.TempDir()
	runDir := t.TempDir()
	dir := t.TempDir()
	// Profile WITHOUT a setup field: this is the path where a learned plan
	// would normally be persisted. A pre-existing pr block lets us prove the
	// rewrite is suppressed end-to-end (the file content stays byte-identical).
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
	withSetupExecutorRunner(t, func(_ context.Context, _ setuppreflight.Options) (*setuppreflight.Result, error) {
		// status=ready but empty successful_commands — exactly the contract
		// the daemon must reject.
		return &setuppreflight.Result{
			Status:            setuppreflight.StatusReady,
			Commands:          []setuppreflight.CommandAttempt{{Run: "echo ok", Source: setuppreflight.SourceDiscovered, ExitCode: 0}},
			ReadinessEvidence: "executor claimed ready without reporting commands",
			Source:            setuppreflight.SourceDiscovered,
			Provider:          "claude",
		}, nil
	})
	res, update, err := runSetupPreflight(context.Background(), setuppreflight.Options{
		Task:                   setupTask(),
		WorkDir:                work,
		RunDir:                 runDir,
		Profiles:               profile.Bundle{Environment: &env},
		EnvironmentProfilePath: envPath,
	})
	if err == nil {
		t.Fatalf("expected setup preflight error; got res=%+v update=%+v", res, update)
	}
	if update != nil {
		t.Fatalf("update should be nil when no learned plan can be persisted: %+v", update)
	}
	// environment.yaml must be byte-identical: not silently left unchanged
	// behind a ready+empty-plan facade.
	after, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("environment.yaml was modified despite missing successful_commands:\nbefore=%s\nafter=%s", before, after)
	}
	// setup_result.json must be diagnostic: status=failed with repair guidance.
	data, readErr := os.ReadFile(filepath.Join(runDir, "setup_result.json"))
	if readErr != nil {
		t.Fatalf("setup_result.json missing: %v", readErr)
	}
	var saved setuppreflight.Result
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatalf("decode setup_result.json: %v", err)
	}
	if saved.Status != setuppreflight.StatusFailed {
		t.Fatalf("saved status got %q, want failed", saved.Status)
	}
	if saved.Error == "" || !strings.Contains(saved.Error, "successful_commands") {
		t.Fatalf("saved error must name the contract violation: %q", saved.Error)
	}
	if saved.RepairGuidance == "" {
		t.Fatalf("saved failure missing repair guidance: %+v", saved)
	}
}

func TestEnforceLearnedSetupPlanContractRequiresReadyEvidence(t *testing.T) {
	base := func() *setuppreflight.Result {
		return &setuppreflight.Result{
			Status:             setuppreflight.StatusReady,
			Commands:           []setuppreflight.CommandAttempt{{Run: "go mod download", Source: setuppreflight.SourceDiscovered, ExitCode: 0}},
			SuccessfulCommands: []profile.SetupCommand{{Run: "go mod download", Why: "fetch modules"}},
			ReadinessEvidence:  "go test ./... passed",
			Source:             setuppreflight.SourceDiscovered,
		}
	}
	tests := []struct {
		name string
		edit func(*setuppreflight.Result)
		want string
	}{
		{
			name: "missing readiness evidence",
			edit: func(res *setuppreflight.Result) { res.ReadinessEvidence = "" },
			want: "readiness_evidence",
		},
		{
			name: "missing source",
			edit: func(res *setuppreflight.Result) { res.Source = "" },
			want: "source",
		},
		{
			name: "successful command did not exit zero",
			edit: func(res *setuppreflight.Result) {
				res.Commands[0].ExitCode = 1
			},
			want: "successful setup command attempt",
		},
		{
			name: "successful command was readiness check only",
			edit: func(res *setuppreflight.Result) {
				res.Commands[0].Source = setuppreflight.SourceReadinessCheck
			},
			want: "successful setup command attempt",
		},
		{
			name: "readiness check source is not canonical plan source",
			edit: func(res *setuppreflight.Result) { res.Source = setuppreflight.SourceReadinessCheck },
			want: "invalid source",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := base()
			tt.edit(res)

			err := setuppreflight.EnforceLearnedPlanContract(res)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("contract error got %v, want mention %q", err, tt.want)
			}
		})
	}
}

// TestSetupResultSchemaMatchesPersistedShape is the JSON/schema validation
// regression for the persisted setup result shape. It writes a representative
// setup result (covering every field the runtime can serialize today),
// re-loads schemas/setup-result.schema.json, and validates the saved JSON
// against it: required keys are present, no extra keys leak (the schema is
// additionalProperties:false), and enum-constrained fields (status, source,
// provider, per-command source) carry values from the schema's enum lists.
// This pins the schema/runtime sync: the runtime persists provider/source
// fields, so the published schema must declare them, guarding against drift
// where the two disagree.
func TestSetupResultSchemaMatchesPersistedShape(t *testing.T) {
	runDir := t.TempDir()
	res := &setuppreflight.Result{
		Status: setuppreflight.StatusReady,
		Commands: []setuppreflight.CommandAttempt{
			{Run: "go mod download", Why: "fetch modules", Source: setuppreflight.SourceDiscovered, ExitCode: 0, StdoutExcerpt: "ok", StderrExcerpt: ""},
			{Run: "go build ./...", Why: "readiness verification", Source: setuppreflight.SourceReadinessCheck, ExitCode: 0},
		},
		SuccessfulCommands: []profile.SetupCommand{{Run: "go mod download", Why: "fetch modules"}},
		InspectedFiles:     []string{"go.mod", "go.sum"},
		ReadinessEvidence:  "discovery passed; readiness verified",
		Provider:           "claude",
		Source:             setuppreflight.SourceDiscovered,
	}
	// Stamp a resolved identity with an explicit empty model so the persisted
	// shape includes executor_* keys (including empty executor_model) that the
	// schema must accept for requeue reuse matching.
	setuppreflight.ApplyExecutorIdentity(res, task.Executor{CLI: "claude", Model: "", Effort: "high"})
	if err := setuppreflight.WriteResult(runDir, res); err != nil {
		t.Fatalf("write setup result: %v", err)
	}
	savedRaw, err := os.ReadFile(filepath.Join(runDir, "setup_result.json"))
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]any
	if err := json.Unmarshal(savedRaw, &saved); err != nil {
		t.Fatalf("decode saved: %v", err)
	}
	if model, ok := saved["executor_model"]; !ok || model != "" {
		t.Fatalf("persisted executor_model must be explicit empty string, got present=%v value=%#v", ok, model)
	}
	schemaPath, err := filepath.Abs("../../schemas/setup-result.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	schemaRaw, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	var schema map[string]any
	if err := json.Unmarshal(schemaRaw, &schema); err != nil {
		t.Fatalf("decode schema: %v", err)
	}
	if errs := validateAgainstSchemaForTest(saved, schema, "$"); len(errs) > 0 {
		t.Fatalf("saved setup_result.json does not match schema:\n  %s", strings.Join(errs, "\n  "))
	}
	// Also assert the schema declares the runtime-persisted provider and
	// source fields. This guards future drift where someone removes one of
	// them from the schema without also removing it from the Go struct.
	props, _ := schema["properties"].(map[string]any)
	for _, requiredKey := range []string{"provider", "source", "successful_commands", "inspected_files", "readiness_evidence", "repair_guidance", "error", "executor_cli", "executor_model", "executor_effort"} {
		if _, ok := props[requiredKey]; !ok {
			t.Fatalf("schema missing property %q that the runtime persists", requiredKey)
		}
	}
}

// validateAgainstSchemaForTest is a focused JSON-schema walker. It covers
// the constraints the persisted setuppreflight.Result schema actually uses today
// (type, required, additionalProperties:false, enum, array items, object
// properties) without pulling in a third-party schema library — Galley's
// go.mod intentionally avoids one. It returns a list of human-readable
// violation messages; an empty slice means the value satisfies the schema.
func validateAgainstSchemaForTest(value any, schema map[string]any, path string) []string {
	var errs []string
	if typ, ok := schema["type"].(string); ok {
		if !jsonTypeMatchesForTest(typ, value) {
			errs = append(errs, fmt.Sprintf("%s: type mismatch: want %s, got %T", path, typ, value))
			return errs
		}
	}
	if enum, ok := schema["enum"].([]any); ok {
		matched := false
		for _, allowed := range enum {
			if allowed == value {
				matched = true
				break
			}
		}
		if !matched {
			errs = append(errs, fmt.Sprintf("%s: value %v not in enum %v", path, value, enum))
		}
	}
	switch v := value.(type) {
	case map[string]any:
		props, _ := schema["properties"].(map[string]any)
		additional, additionalSpecified := schema["additionalProperties"]
		if required, ok := schema["required"].([]any); ok {
			for _, r := range required {
				name, _ := r.(string)
				if _, has := v[name]; !has {
					errs = append(errs, fmt.Sprintf("%s: missing required property %q", path, name))
				}
			}
		}
		for key, child := range v {
			subSchema, declared := props[key].(map[string]any)
			if !declared {
				if additionalSpecified {
					if allow, ok := additional.(bool); ok && !allow {
						errs = append(errs, fmt.Sprintf("%s: property %q is not declared in schema and additionalProperties is false", path, key))
					}
				}
				continue
			}
			errs = append(errs, validateAgainstSchemaForTest(child, subSchema, path+"."+key)...)
		}
	case []any:
		if items, ok := schema["items"].(map[string]any); ok {
			for i, child := range v {
				errs = append(errs, validateAgainstSchemaForTest(child, items, fmt.Sprintf("%s[%d]", path, i))...)
			}
		}
	}
	return errs
}

func jsonTypeMatchesForTest(want string, value any) bool {
	switch want {
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "string":
		_, ok := value.(string)
		return ok
	case "integer":
		// encoding/json decodes numbers as float64; accept whole-number floats.
		if f, ok := value.(float64); ok {
			return f == float64(int64(f))
		}
		return false
	case "number":
		_, ok := value.(float64)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "null":
		return value == nil
	}
	return true
}
