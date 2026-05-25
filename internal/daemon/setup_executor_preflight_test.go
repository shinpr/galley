package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/shinpr/galley/internal/profile"
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

// TestSetupPreflightRunsBeforeAcceptanceSkeletonAndExecutor proves AC2: the
// setup preflight runs after worktree/input-file preparation and before any
// acceptance skeleton work or implementation executor attempt. The setup
// executor runner stub records its observed order vs the daemon-level
// acceptance_skeleton_creator subprocess.
func TestSetupPreflightSequencesBeforeSkeletonAndExecutor(t *testing.T) {
	// Authored setup plan that writes a sentinel file BEFORE skeleton creation.
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
echo '{"status":"completed","summary":"done","files_modified":["daemon-output.txt"],"acceptance_criteria":[{"id":"AC1","status":"satisfied","evidence":["diff"],"notes":"done"}],"verification":[],"decisions":[],"risks":[]}'
`)

	// Authored environment profile with a setup plan whose first command
	// writes the sentinel inside the worktree. When the daemon-level setup
	// preflight runs the authored plan it creates setup.sentinel before
	// AcceptanceSkeletonPreflight is invoked, which is exactly the ordering
	// AC2 requires. Because the authored plan succeeds, persistLearnedSetupPlan
	// is never reached and the verification excerpt records
	// "environment.yaml=unchanged".
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
	// Defensive: if anything changes the path so the daemon falls back to
	// discovery, fail loudly rather than silently masking AC2.
	withSetupExecutorRunner(t, func(_ context.Context, _ SetupExecutorPreflightOptions) (*SetupResult, error) {
		t.Fatalf("setup executor runner should not be invoked when authored environment.setup plan succeeds")
		return nil, nil
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

	if err := Run(context.Background(), Options{
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
	withSetupExecutorRunner(t, func(_ context.Context, opts SetupExecutorPreflightOptions) (*SetupResult, error) {
		cap.Commands = map[string]string{}
		if opts.Profiles.Environment != nil {
			for k, v := range opts.Profiles.Environment.Commands {
				cap.Commands[k] = v
			}
		}
		signals := opts.RepositorySignals
		if signals == nil {
			signals = discoverRepositorySignals(opts.WorkDir)
		}
		cap.Signals = append([]string{}, signals...)
		cap.WorkDir = opts.WorkDir
		return &SetupResult{
			Status:             SetupStatusReady,
			Commands:           []SetupCommandAttempt{{Run: "go mod download", Source: SetupSourceDiscovered, ExitCode: 0}},
			SuccessfulCommands: []profile.SetupCommand{{Run: "go mod download", Why: "fetch modules"}},
			ReadinessEvidence:  "stub readiness",
			Source:             SetupSourceDiscovered,
			Provider:           "claude",
		}, nil
	})
	res, _, err := SetupExecutorPreflight(context.Background(), SetupExecutorPreflightOptions{
		Task:     setupTask(),
		WorkDir:  work,
		RunDir:   runDir,
		Profiles: profile.Bundle{Environment: env},
	})
	if err != nil {
		t.Fatalf("setup preflight: %v", err)
	}
	if res == nil || res.Status != SetupStatusReady {
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

// TestSetupPreflightAuthoredPlanUsesRequiredCheckShellPath proves the authored
// setup path and the daemon-authored readiness check share the same
// profile-owned shell selection contract as required checks. A prior regression
// used a setup-only bash/sh lookup and ignored required_checks.shell_path.
func TestSetupPreflightAuthoredPlanUsesRequiredCheckShellPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX fake shell script")
	}
	work := t.TempDir()
	runDir := t.TempDir()
	fakeShell := filepath.Join(t.TempDir(), "bash")
	marker := filepath.Join(t.TempDir(), "shell.invoked")
	if err := os.WriteFile(fakeShell, []byte("#!/bin/sh\nprintf invoked >> "+marker+"\nexec /bin/sh \"$@\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	env := &profile.Environment{
		ID:  "shell-path",
		CWD: work,
		RequiredChecks: profile.RequiredCheckEnvironment{
			ShellPath: fakeShell,
		},
		Setup: &profile.SetupPlan{Commands: []profile.SetupCommand{
			{Run: "printf setup > setup.proof", Why: "prove setup shell"},
		}},
		Constraints: profile.Constraints{
			Network:             "approval_required",
			SecretsPolicy:       "never_read_env_files",
			DestructiveCommands: "deny",
		},
	}
	quality := &profile.Quality{
		ID: "quality",
		RequiredChecks: []profile.RequiredCheck{{
			ID:                "proof",
			PreferredCommands: []string{"test -f setup.proof"},
			Required:          true,
		}},
		PassPolicy: profile.PassPolicy{MinScore: 1},
	}
	res, _, err := SetupExecutorPreflight(context.Background(), SetupExecutorPreflightOptions{
		Task:     setupTask(),
		WorkDir:  work,
		RunDir:   runDir,
		Profiles: profile.Bundle{Environment: env, Quality: quality},
	})
	if err != nil {
		t.Fatalf("setup preflight: %v", err)
	}
	if res == nil || res.Status != SetupStatusReady {
		t.Fatalf("setup result: %+v", res)
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("fake shell marker missing: %v", err)
	}
	if got := strings.Count(string(data), "invoked"); got != 2 {
		t.Fatalf("fake shell invocations got %d, want setup command and readiness check", got)
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
	claudeOpts := SetupExecutorPreflightOptions{
		Task:      setupTask(),
		WorkDir:   work,
		RunDir:    runDir,
		Profiles:  profile.Bundle{},
		ClaudeBin: "/path/to/claude",
	}
	claudePlan, provider, err := buildSetupExecutorCommandPlan(claudeOpts, payload)
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
	codexOpts := SetupExecutorPreflightOptions{
		Task:     codexTask,
		WorkDir:  work,
		RunDir:   runDir,
		Profiles: profile.Bundle{},
		CodexBin: "/path/to/codex",
	}
	codexPlan, provider, err := buildSetupExecutorCommandPlan(codexOpts, payload)
	if err != nil {
		t.Fatalf("codex plan: %v", err)
	}
	if provider != "codex" {
		t.Fatalf("provider got %q", provider)
	}
	if len(codexPlan.Argv) == 0 || codexPlan.Argv[0] != "/path/to/codex" {
		t.Fatalf("codex argv[0] got %v", codexPlan.Argv)
	}
	if len(codexPlan.Env) == 0 {
		t.Fatalf("codex setup executor must run with runner restricted env")
	}
	joined := strings.Join(codexPlan.Argv, " ")
	if !strings.Contains(joined, "--output-last-message") {
		t.Fatalf("codex argv missing --output-last-message: %v", codexPlan.Argv)
	}
	if !strings.Contains(joined, runner.CodexLastMessageFilename) {
		t.Fatalf("codex argv missing capture filename: %v", codexPlan.Argv)
	}
}

// TestSetupExecutorResolveResultClaudeAndCodex proves the second part of AC4:
// the result-parsing path accepts the same SetupResult JSON shape from both
// providers — Claude via stdout tail, Codex via the attempt-scoped
// --output-last-message file.
func TestSetupExecutorResolveResultClaudeAndCodex(t *testing.T) {
	runDir := t.TempDir()
	claudeJSON := `{"status":"ready","commands":[{"run":"go mod download","source":"discovered","exit_code":0}],"successful_commands":[{"run":"go mod download","why":"fetch modules"}],"readiness_evidence":"ok"}`
	// Claude path: parses arbitrary tail text that contains the JSON object.
	claudeOpts := SetupExecutorPreflightOptions{Task: setupTask(), RunDir: runDir}
	claude, err := resolveSetupExecutorResult(claudeOpts, "noise prefix\n"+claudeJSON+"\nnoise suffix")
	if err != nil {
		t.Fatalf("claude resolve: %v", err)
	}
	if claude.Status != SetupStatusReady || len(claude.Commands) != 1 || claude.Commands[0].Source != SetupSourceDiscovered {
		t.Fatalf("claude parsed result: %+v", claude)
	}

	// Codex path: read from runner.CodexLastMessageFilename written in runDir.
	codexJSON := `Some preamble text\n` + claudeJSON
	if err := os.WriteFile(filepath.Join(runDir, runner.CodexLastMessageFilename), []byte(codexJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	codexTask := setupTask()
	codexTask.Executor.CLI = "codex"
	codexOpts := SetupExecutorPreflightOptions{Task: codexTask, RunDir: runDir}
	codex, err := resolveSetupExecutorResult(codexOpts, "")
	if err != nil {
		t.Fatalf("codex resolve: %v", err)
	}
	if codex.Status != SetupStatusReady || codex.SuccessfulCommands[0].Run != "go mod download" {
		t.Fatalf("codex parsed result: %+v", codex)
	}
}

// TestSetupPreflightStaleAuthoredPlanFallsBackToDiscovery proves AC6: a stale
// authored setup plan whose first command fails causes the daemon to fall back
// to the setup executor's discovery, and both the failed authored command and
// the successful discovered command are recorded in setup_result.json with
// their distinct sources.
func TestSetupPreflightStaleAuthoredPlanFallsBackToDiscovery(t *testing.T) {
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
	withSetupExecutorRunner(t, func(_ context.Context, opts SetupExecutorPreflightOptions) (*SetupResult, error) {
		return &SetupResult{
			Status:             SetupStatusReady,
			Commands:           []SetupCommandAttempt{{Run: "echo discovered", Source: SetupSourceDiscovered, ExitCode: 0}},
			SuccessfulCommands: []profile.SetupCommand{{Run: "echo discovered", Why: "actual working command"}},
			ReadinessEvidence:  "discovered plan passed",
			Source:             SetupSourceDiscovered,
			Provider:           "claude",
		}, nil
	})
	res, update, err := SetupExecutorPreflight(context.Background(), SetupExecutorPreflightOptions{
		Task:                   setupTask(),
		WorkDir:                work,
		RunDir:                 runDir,
		Profiles:               profile.Bundle{Environment: &env},
		EnvironmentProfilePath: envPath,
	})
	if err != nil {
		t.Fatalf("setup preflight: %v", err)
	}
	if res == nil || res.Status != SetupStatusReady {
		t.Fatalf("result: %+v", res)
	}
	// AC6: both the failed authored command AND the successful discovered
	// command must be recorded.
	foundFailedAuthored := false
	foundSuccessfulDiscovered := false
	for _, c := range res.Commands {
		if c.Source == SetupSourceEnvironmentSetup && c.Run == "false" && c.ExitCode != 0 {
			foundFailedAuthored = true
		}
		if c.Source == SetupSourceDiscovered && c.Run == "echo discovered" && c.ExitCode == 0 {
			foundSuccessfulDiscovered = true
		}
	}
	if !foundFailedAuthored {
		t.Fatalf("failed authored command not recorded: %+v", res.Commands)
	}
	if !foundSuccessfulDiscovered {
		t.Fatalf("successful discovered command not recorded: %+v", res.Commands)
	}
	_ = update
	// AC7: persistence-only check is in another test, but make sure update
	// metadata reflects that the prior plan differed.
	if update == nil || !update.Changed || update.Before == nil || len(update.Before.Commands) == 0 || update.Before.Commands[0].Run != "false" {
		t.Fatalf("environment update should record prior stale plan: %+v", update)
	}
}

// TestSetupPreflightAtomicProfileRewriteAndSecondRunReuse proves AC7: a
// successful learned plan atomically rewrites environment.yaml setup while
// preserving unrelated content, and a second daemon run with the persisted
// setup uses the authored plan without invoking discovery.
func TestSetupPreflightAtomicProfileRewriteAndSecondRunReuse(t *testing.T) {
	work := t.TempDir()
	dir := t.TempDir()
	// Profile without setup; includes unrelated content that must survive the
	// atomic rewrite. pr.base is intentionally written as an unquoted scalar
	// so the round-trip check below proves the YAML node style is preserved
	// (a previous regression re-quoted it on rewrite).
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
	discoveryCalls := 0
	withSetupExecutorRunner(t, func(_ context.Context, _ SetupExecutorPreflightOptions) (*SetupResult, error) {
		discoveryCalls++
		return &SetupResult{
			Status:             SetupStatusReady,
			Commands:           []SetupCommandAttempt{{Run: "echo learned", Source: SetupSourceDiscovered, ExitCode: 0}},
			SuccessfulCommands: []profile.SetupCommand{{Run: "echo learned", Why: "learned setup"}},
			ReadinessEvidence:  "discovery passed",
			Source:             SetupSourceDiscovered,
			Provider:           "claude",
		}, nil
	})
	res1, update1, err := SetupExecutorPreflight(context.Background(), SetupExecutorPreflightOptions{
		Task:                   setupTask(),
		WorkDir:                work,
		RunDir:                 runDir1,
		Profiles:               profile.Bundle{Environment: &env1},
		EnvironmentProfilePath: envPath,
	})
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if discoveryCalls != 1 {
		t.Fatalf("discoveryCalls first run got %d, want 1", discoveryCalls)
	}
	if res1 == nil || res1.Status != SetupStatusReady {
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

	// AC7 second-run reuse: re-running preflight with the persisted plan must
	// NOT invoke discovery and must not record any new environment.yaml change.
	runDir2 := t.TempDir()
	res2, update2, err := SetupExecutorPreflight(context.Background(), SetupExecutorPreflightOptions{
		Task:                   setupTask(),
		WorkDir:                work,
		RunDir:                 runDir2,
		Profiles:               profile.Bundle{Environment: &env2},
		EnvironmentProfilePath: envPath,
	})
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if discoveryCalls != 1 {
		t.Fatalf("discovery was re-invoked on second run; discoveryCalls=%d", discoveryCalls)
	}
	if res2 == nil || res2.Status != SetupStatusReady {
		t.Fatalf("second run result: %+v", res2)
	}
	if update2 != nil {
		t.Fatalf("second run should not have rewritten environment.yaml: %+v", update2)
	}
	// All recorded commands should be from the authored (now persisted) source.
	for _, c := range res2.Commands {
		if c.Source == SetupSourceDiscovered {
			t.Fatalf("second run recorded discovered command but should reuse persisted plan: %+v", res2.Commands)
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
	withSetupExecutorRunner(t, func(_ context.Context, _ SetupExecutorPreflightOptions) (*SetupResult, error) {
		return &SetupResult{
			Status:         SetupStatusFailed,
			Commands:       []SetupCommandAttempt{{Run: "npm ci", Source: SetupSourceDiscovered, ExitCode: 1, StderrExcerpt: "ENOENT"}},
			Error:          "setup executor could not install dependencies",
			RepairGuidance: "set environment.commands.install to a working command, or author environment.setup",
			Source:         SetupSourceDiscovered,
			Provider:       "claude",
		}, nil
	})
	_, _, err := SetupExecutorPreflight(context.Background(), SetupExecutorPreflightOptions{
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
	var saved SetupResult
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatalf("decode setup_result.json: %v", err)
	}
	if saved.Status != SetupStatusFailed {
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
	appendFailureAttempt(&tk, SetupPhase, SetupFailedKind, err, runDir)
	if len(tk.Attempts) == 0 || tk.Attempts[len(tk.Attempts)-1].Error == nil {
		t.Fatalf("no attempt error appended")
	}
	last := tk.Attempts[len(tk.Attempts)-1].Error
	if last.Phase != SetupPhase || last.Kind != SetupFailedKind {
		t.Fatalf("phase/kind got phase=%q kind=%q want phase=%q kind=%q", last.Phase, last.Kind, SetupPhase, SetupFailedKind)
	}
}

// TestSetupPreflightExcludesAcceptanceSkeletonReadiness proves AC10: setup
// readiness checks do not include acceptance-skeleton obligations. The
// authored-plan readiness check picks a required quality check; with no
// skeleton-related required checks defined, the readiness check should succeed
// without depending on any task-specific skeleton file.
func TestSetupPreflightExcludesAcceptanceSkeletonReadiness(t *testing.T) {
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
	res, _, err := SetupExecutorPreflight(context.Background(), SetupExecutorPreflightOptions{
		Task:     tk,
		WorkDir:  work,
		RunDir:   runDir,
		Profiles: profile.Bundle{Environment: env, Quality: quality},
	})
	if err != nil {
		t.Fatalf("setup preflight: %v", err)
	}
	if res == nil || res.Status != SetupStatusReady {
		t.Fatalf("setup result: %+v", res)
	}
	// The recorded commands must include the readiness check entry with the
	// daemon-only source value `readiness_check` (AC10 + schema fix).
	foundReadiness := false
	for _, c := range res.Commands {
		if c.Source == SetupSourceReadinessCheck {
			foundReadiness = true
			if c.ExitCode != 0 {
				t.Fatalf("readiness check failed: %+v", c)
			}
		}
		if strings.Contains(c.Run, "internal/foo/foo_test.go") {
			t.Fatalf("readiness check executed task-specific skeleton command: %+v", c)
		}
	}
	if !foundReadiness {
		t.Fatalf("readiness check entry missing from commands: %+v", res.Commands)
	}
	// The worktree directory must not contain any skeleton files written by
	// setup; only the authored plan ran.
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
// is the regression test for the setup-result contract update: a setup
// executor that returns status=ready with no successful_commands cannot
// produce a learned plan, so the daemon must downgrade the result to failed
// (with repair guidance), keep setup_result.json diagnostic, and NOT silently
// leave environment.yaml unchanged behind a fake-passing setup. The previous
// behavior silently skipped persistence and treated the result as ready,
// which violated AC7's "environment.yaml is not silently left unchanged"
// invariant.
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
	withSetupExecutorRunner(t, func(_ context.Context, _ SetupExecutorPreflightOptions) (*SetupResult, error) {
		// status=ready but empty successful_commands — exactly the contract
		// the daemon must reject.
		return &SetupResult{
			Status:            SetupStatusReady,
			Commands:          []SetupCommandAttempt{{Run: "echo ok", Source: SetupSourceDiscovered, ExitCode: 0}},
			ReadinessEvidence: "executor claimed ready without reporting commands",
			Source:            SetupSourceDiscovered,
			Provider:          "claude",
		}, nil
	})
	res, update, err := SetupExecutorPreflight(context.Background(), SetupExecutorPreflightOptions{
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
	// behind a ready+empty-plan facade. The contract is "no silent unchanged".
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
	var saved SetupResult
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatalf("decode setup_result.json: %v", err)
	}
	if saved.Status != SetupStatusFailed {
		t.Fatalf("saved status got %q, want failed", saved.Status)
	}
	if saved.Error == "" || !strings.Contains(saved.Error, "successful_commands") {
		t.Fatalf("saved error must name the contract violation: %q", saved.Error)
	}
	if saved.RepairGuidance == "" {
		t.Fatalf("saved failure missing repair guidance: %+v", saved)
	}
}

// TestSetupResultSchemaMatchesPersistedShape is the JSON/schema validation
// regression for the persisted SetupResult shape. It writes a representative
// SetupResult (covering every field the runtime can serialize today),
// re-loads schemas/setup-result.schema.json, and validates the saved JSON
// against it: required keys are present, no extra keys leak (the schema is
// additionalProperties:false), and enum-constrained fields (status, source,
// provider, per-command source) carry values from the schema's enum lists.
// This pins the schema/runtime sync the previous contract drift would have
// allowed (the runtime persisted provider/source fields that the published
// schema did not declare).
func TestSetupResultSchemaMatchesPersistedShape(t *testing.T) {
	runDir := t.TempDir()
	res := &SetupResult{
		Status: SetupStatusReady,
		Commands: []SetupCommandAttempt{
			{Run: "go mod download", Why: "fetch modules", Source: SetupSourceDiscovered, ExitCode: 0, StdoutExcerpt: "ok", StderrExcerpt: ""},
			{Run: "go build ./...", Why: "readiness verification", Source: SetupSourceReadinessCheck, ExitCode: 0},
		},
		SuccessfulCommands: []profile.SetupCommand{{Run: "go mod download", Why: "fetch modules"}},
		InspectedFiles:     []string{"go.mod", "go.sum"},
		ReadinessEvidence:  "discovery passed; readiness verified",
		Provider:           "claude",
		Source:             SetupSourceDiscovered,
	}
	if err := WriteSetupResult(runDir, res); err != nil {
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
	// Also assert the schema declares the runtime-persisted fields the
	// previous contract drift omitted (provider, source). This guards future
	// drift where someone removes one of them from the schema without also
	// removing it from the Go struct.
	props, _ := schema["properties"].(map[string]any)
	for _, requiredKey := range []string{"provider", "source", "successful_commands", "inspected_files", "readiness_evidence", "repair_guidance", "error"} {
		if _, ok := props[requiredKey]; !ok {
			t.Fatalf("schema missing property %q that the runtime persists", requiredKey)
		}
	}
}

// validateAgainstSchemaForTest is a focused JSON-schema walker. It covers
// the constraints the persisted SetupResult schema actually uses today
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
