package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	setuppreflight "github.com/shinpr/galley/internal/preflight/setup"
	"github.com/shinpr/galley/internal/profile"
	"github.com/shinpr/galley/internal/supervisor"
	"github.com/shinpr/galley/internal/task"
)

func TestDaemonRejectsInvalidEffectiveExecutorBeforeProviderRoles(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	promptPath, schemaPath := writeDaemonPromptFiles(t)

	var providerStarts atomic.Int32
	claudeBin := writeFakeCommand(t, "claude", "echo should-not-run >&2\nexit 1\n")
	codexBin := writeFakeCommand(t, "codex", "echo should-not-run >&2\nexit 1\n")

	envPath := writeSetupEnvironmentProfile(t, t.TempDir(), `id: "preflight"
cwd: `+workdirQuote(repo)+`
commands: {}
executor:
  default_cli: "grok"
  effort: "none"
constraints:
  network: "approval_required"
  secrets_policy: "never_read_env_files"
  destructive_commands: "deny"
`)

	withSetupExecutorRunner(t, func(context.Context, setuppreflight.Options) (*setuppreflight.Result, error) {
		providerStarts.Add(1)
		return &setuppreflight.Result{Status: setuppreflight.StatusReady}, nil
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
	loaded.Executor = task.Executor{CLI: "claude"}
	if err := task.Save(taskPath, loaded); err != nil {
		t.Fatal(err)
	}

	_ = runTestDaemon(context.Background(), Options{
		Root:                   root,
		SystemPromptFile:       promptPath,
		JSONSchemaFile:         schemaPath,
		EnvironmentProfileFile: envPath,
		Once:                   true,
		MaxConcurrentTasks:     1,
		Supervisor:             "claude",
		ClaudeBin:              claudeBin,
		CodexBin:               codexBin,
	})

	if providerStarts.Load() != 0 {
		t.Fatalf("setup provider started %d times; expected zero", providerStarts.Load())
	}
	failedPath := filepath.Join(root, "tasks", "failed", "task.yaml")
	failed, err := task.Load(failedPath)
	if err != nil {
		t.Fatalf("expected failed task at %s: %v", failedPath, err)
	}
	if failed.Status != "failed" {
		t.Fatalf("status got %q, want failed", failed.Status)
	}
	if len(failed.Attempts) == 0 || failed.Attempts[0].Error == nil {
		t.Fatalf("expected failure attempt evidence, got %#v", failed.Attempts)
	}
	if failed.Attempts[0].Error.Phase != "executor_preflight" {
		t.Fatalf("phase got %q, want executor_preflight", failed.Attempts[0].Error.Phase)
	}
	if failed.Attempts[0].Error.Kind != "executor_config_failed" {
		t.Fatalf("kind got %q, want executor_config_failed", failed.Attempts[0].Error.Kind)
	}
	if !strings.Contains(failed.Attempts[0].Error.Message, "executor.effort for claude") {
		t.Fatalf("message got %q", failed.Attempts[0].Error.Message)
	}
	if failed.Executor.Effort != "" {
		t.Fatalf("failed task must not write environment effort back, got %#v", failed.Executor)
	}
	if failed.Executor.CLI != "claude" {
		t.Fatalf("failed task cli got %q, want claude", failed.Executor.CLI)
	}
}

func TestDaemonUsesSameEffectiveExecutorAcrossRoles(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	promptPath, schemaPath := writeDaemonPromptFiles(t)

	type observed struct {
		CLI    string
		Model  string
		Effort string
	}
	var setupObs observed
	var supervisorExecutor task.Executor

	envPath := writeSetupEnvironmentProfile(t, t.TempDir(), `id: "shared-effective"
cwd: `+workdirQuote(repo)+`
commands:
  test_unit: "true"
executor:
  default_cli: "codex"
  model: "env-model"
  effort: "minimal"
constraints:
  network: "approval_required"
  secrets_policy: "never_read_env_files"
  destructive_commands: "deny"
setup:
  commands:
    - run: "true"
      why: "no-op setup"
`)

	withSetupExecutorRunner(t, func(_ context.Context, opts setuppreflight.Options) (*setuppreflight.Result, error) {
		setupObs = observed{
			CLI:    opts.Task.Executor.CLI,
			Model:  opts.Task.Executor.Model,
			Effort: opts.Task.Executor.Effort,
		}
		return &setuppreflight.Result{
			Status: setuppreflight.StatusReady,
			Commands: []setuppreflight.CommandAttempt{{
				Run:      "true",
				Why:      "no-op setup",
				Source:   setuppreflight.SourceEnvironmentSetup,
				ExitCode: 0,
			}},
			SuccessfulCommands: []profile.SetupCommand{{
				Run: "true",
				Why: "no-op setup",
			}},
			ReadinessEvidence: "setup observed effective executor",
			Source:            setuppreflight.SourceEnvironmentSetup,
			Provider:          opts.Task.Executor.CLI,
		}, nil
	})

	creatorManifest := `{"outputs":[{"ac_id":"AC1","path":"internal/foo/foo_test.go","kind":"go-test","purpose":"verify AC1","satisfies":"AC1 observable behavior","integration_point":"executor completes this skeleton before acceptance","implementation_required":true}],"no_skeletons":[]}`
	executorResult := `{"status":"completed","summary":"done","files_modified":["daemon-output.txt","internal/foo/foo_test.go"],"acceptance_criteria":[{"id":"AC1","status":"satisfied","evidence":["diff"],"notes":"done"}],"verification":[],"scope_expansions":[],"decisions":[],"risks":[]}`
	// The fake distinguishes skeleton and implementation through their prompts.
	codexBin := writeFakeCommand(t, "codex", `out=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --output-last-message)
      out="$2"
      shift 2
      ;;
    *)
      shift
      ;;
  esac
done
input="$(cat)"
case "$input" in
  *"acceptance skeleton creator"*)
    mkdir -p internal/foo
    printf 'package foo_test\n' > internal/foo/foo_test.go
    printf '%s\n' '`+creatorManifest+`' > "$out"
    ;;
  *)
    echo change > daemon-output.txt
    printf '%s\n' '`+executorResult+`' > "$out"
    printf '%s\n' '{"type":"turn.completed","usage":{}}'
    ;;
esac
`)
	claudeBin := writeFakeClaude(t, "exit 1\n")
	supervisorRunner := func(_ context.Context, _ Options, evidence supervisor.Evidence, _, _ string) (supervisor.Verdict, error) {
		supervisorExecutor = evidence.Task.Executor
		return supervisor.Verdict{Status: "accepted", Summary: "effective executor observed"}, nil
	}

	taskPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, taskPath, repo)
	loaded, err := task.Load(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Executor = task.Executor{Model: "task-model"}
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
		CodexBin:               codexBin,
		dependencies:           &daemonDependencies{supervisorRunner: supervisorRunner},
	}); err != nil {
		t.Fatal(err)
	}

	want := observed{CLI: "codex", Model: "task-model", Effort: "minimal"}
	if setupObs != want {
		t.Fatalf("setup effective executor = %#v, want %#v", setupObs, want)
	}
	if supervisorExecutor != (task.Executor{CLI: want.CLI, Model: want.Model, Effort: want.Effort}) {
		t.Fatalf("supervisor effective executor = %#v, want %#v", supervisorExecutor, want)
	}

	effectiveMatches, err := filepath.Glob(filepath.Join(root, "runs", "*", "task.effective.yaml"))
	if err != nil || len(effectiveMatches) != 1 {
		t.Fatalf("task.effective.yaml glob = %v (err %v)", effectiveMatches, err)
	}
	effectiveSnapshot, err := task.Load(effectiveMatches[0])
	if err != nil {
		t.Fatal(err)
	}
	if effectiveSnapshot.Executor != (task.Executor{CLI: want.CLI, Model: want.Model, Effort: want.Effort}) {
		t.Fatalf("run effective executor = %#v, want %#v", effectiveSnapshot.Executor, want)
	}

	matches, err := filepath.Glob(filepath.Join(root, "runs", "*", "preflight_creator_command_plan.json"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("preflight_creator_command_plan.json glob = %v (err %v)", matches, err)
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	var plan struct {
		Argv []string `json:"argv"`
	}
	if err := json.Unmarshal(data, &plan); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(plan.Argv, " ")
	if filepath.Base(plan.Argv[0]) != "codex" {
		t.Fatalf("skeleton creator cli = %v, want codex", plan.Argv)
	}
	if !strings.Contains(joined, "task-model") {
		t.Fatalf("skeleton creator missing effective model: %v", plan.Argv)
	}
	if !strings.Contains(joined, `model_reasoning_effort="minimal"`) && !strings.Contains(joined, "model_reasoning_effort=minimal") {
		t.Fatalf("skeleton creator missing effective effort: %v", plan.Argv)
	}

	donePath := filepath.Join(root, "tasks", "done", "task.yaml")
	done, err := task.Load(donePath)
	if err != nil {
		t.Fatalf("expected done task: %v", err)
	}
	if done.Executor.CLI != "" || done.Executor.Effort != "" || done.Executor.Model != "task-model" {
		t.Fatalf("authored executor must stay partial after run, got %#v", done.Executor)
	}
	foundCodex := false
	for _, vc := range done.Verification.Commands {
		if strings.Contains(vc.Cmd, "codex") {
			foundCodex = true
			break
		}
	}
	if !foundCodex {
		t.Fatalf("implementation verification missing codex command: %#v", done.Verification.Commands)
	}
}

func TestDaemonEmptyEffortDelegatesToProviderAcrossRoles(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	promptPath, schemaPath := writeDaemonPromptFiles(t)

	type observed struct {
		CLI    string
		Model  string
		Effort string
	}
	var setupObs observed
	var supervisorExecutor task.Executor

	envPath := writeSetupEnvironmentProfile(t, t.TempDir(), `id: "empty-effort"
cwd: `+workdirQuote(repo)+`
commands:
  test_unit: "true"
executor:
  default_cli: "codex"
constraints:
  network: "approval_required"
  secrets_policy: "never_read_env_files"
  destructive_commands: "deny"
setup:
  commands:
    - run: "true"
      why: "no-op setup"
`)

	withSetupExecutorRunner(t, func(_ context.Context, opts setuppreflight.Options) (*setuppreflight.Result, error) {
		setupObs = observed{
			CLI:    opts.Task.Executor.CLI,
			Model:  opts.Task.Executor.Model,
			Effort: opts.Task.Executor.Effort,
		}
		return &setuppreflight.Result{
			Status: setuppreflight.StatusReady,
			Commands: []setuppreflight.CommandAttempt{{
				Run:      "true",
				Why:      "no-op setup",
				Source:   setuppreflight.SourceEnvironmentSetup,
				ExitCode: 0,
			}},
			SuccessfulCommands: []profile.SetupCommand{{
				Run: "true",
				Why: "no-op setup",
			}},
			ReadinessEvidence: "setup observed empty effort",
			Source:            setuppreflight.SourceEnvironmentSetup,
			Provider:          opts.Task.Executor.CLI,
		}, nil
	})

	creatorManifest := `{"outputs":[{"ac_id":"AC1","path":"internal/foo/foo_test.go","kind":"go-test","purpose":"verify AC1","satisfies":"AC1 observable behavior","integration_point":"executor completes this skeleton before acceptance","implementation_required":true}],"no_skeletons":[]}`
	executorResult := `{"status":"completed","summary":"done","files_modified":["daemon-output.txt","internal/foo/foo_test.go"],"acceptance_criteria":[{"id":"AC1","status":"satisfied","evidence":["diff"],"notes":"done"}],"verification":[],"scope_expansions":[],"decisions":[],"risks":[]}`
	codexBin := writeFakeCommand(t, "codex", `out=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --output-last-message)
      out="$2"
      shift 2
      ;;
    *)
      shift
      ;;
  esac
done
input="$(cat)"
case "$input" in
  *"acceptance skeleton creator"*)
    mkdir -p internal/foo
    printf 'package foo_test\n' > internal/foo/foo_test.go
    printf '%s\n' '`+creatorManifest+`' > "$out"
    ;;
  *)
    echo change > daemon-output.txt
    printf '%s\n' '`+executorResult+`' > "$out"
    printf '%s\n' '{"type":"turn.completed","usage":{}}'
    ;;
esac
`)
	claudeBin := writeFakeClaude(t, "exit 1\n")
	supervisorRunner := func(_ context.Context, _ Options, evidence supervisor.Evidence, _, _ string) (supervisor.Verdict, error) {
		supervisorExecutor = evidence.Task.Executor
		return supervisor.Verdict{Status: "accepted", Summary: "empty effort observed"}, nil
	}

	taskPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, taskPath, repo)
	loaded, err := task.Load(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Executor = task.Executor{}
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
		CodexBin:               codexBin,
		dependencies:           &daemonDependencies{supervisorRunner: supervisorRunner},
	}); err != nil {
		t.Fatal(err)
	}

	want := observed{CLI: "codex", Model: "", Effort: ""}
	if setupObs != want {
		t.Fatalf("setup effective executor = %#v, want %#v", setupObs, want)
	}
	if supervisorExecutor != (task.Executor{CLI: "codex"}) {
		t.Fatalf("supervisor effective executor = %#v, want empty effort codex", supervisorExecutor)
	}

	effectiveMatches, err := filepath.Glob(filepath.Join(root, "runs", "*", "task.effective.yaml"))
	if err != nil || len(effectiveMatches) != 1 {
		t.Fatalf("task.effective.yaml glob = %v (err %v)", effectiveMatches, err)
	}
	effectiveSnapshot, err := task.Load(effectiveMatches[0])
	if err != nil {
		t.Fatal(err)
	}
	if effectiveSnapshot.Executor != (task.Executor{CLI: "codex"}) {
		t.Fatalf("run effective executor = %#v, want empty effort codex", effectiveSnapshot.Executor)
	}

	setupMatches, err := filepath.Glob(filepath.Join(root, "runs", "*", "setup_result.json"))
	if err != nil || len(setupMatches) != 1 {
		t.Fatalf("setup_result.json glob = %v (err %v)", setupMatches, err)
	}
	setupData, err := os.ReadFile(setupMatches[0])
	if err != nil {
		t.Fatal(err)
	}
	var setupResult setuppreflight.Result
	if err := json.Unmarshal(setupData, &setupResult); err != nil {
		t.Fatal(err)
	}
	if setupResult.ExecutorEffort != "" {
		t.Fatalf("setup evidence must persist empty effort identity, got %q", setupResult.ExecutorEffort)
	}

	matches, err := filepath.Glob(filepath.Join(root, "runs", "*", "preflight_creator_command_plan.json"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("preflight_creator_command_plan.json glob = %v (err %v)", matches, err)
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	var plan struct {
		Argv []string `json:"argv"`
	}
	if err := json.Unmarshal(data, &plan); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(plan.Argv, " ")
	if strings.Contains(joined, "model_reasoning_effort") {
		t.Fatalf("empty effort must not add a codex reasoning-effort override: %v", plan.Argv)
	}
}

func TestDaemonRequeuePicksUpChangedEnvironmentExecutorDefaults(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	promptPath, schemaPath := writeDaemonPromptFiles(t)

	type observed struct {
		CLI    string
		Model  string
		Effort string
	}
	var setupCalls []observed

	envDir := t.TempDir()
	envBody := func(cli, model, effort string) string {
		return `id: "requeue-env"
cwd: ` + workdirQuote(repo) + `
commands:
  test_unit: "true"
executor:
  default_cli: "` + cli + `"
  model: "` + model + `"
  effort: "` + effort + `"
constraints:
  network: "approval_required"
  secrets_policy: "never_read_env_files"
  destructive_commands: "deny"
setup:
  commands:
    - run: "true"
      why: "no-op setup"
worktree:
  cleanup: false
`
	}
	envPath := writeSetupEnvironmentProfile(t, envDir, envBody("codex", "env-model-v1", "minimal"))

	withSetupExecutorRunner(t, func(_ context.Context, opts setuppreflight.Options) (*setuppreflight.Result, error) {
		setupCalls = append(setupCalls, observed{
			CLI:    opts.Task.Executor.CLI,
			Model:  opts.Task.Executor.Model,
			Effort: opts.Task.Executor.Effort,
		})
		return &setuppreflight.Result{
			Status: setuppreflight.StatusReady,
			Commands: []setuppreflight.CommandAttempt{{
				Run:      "true",
				Why:      "no-op setup",
				Source:   setuppreflight.SourceEnvironmentSetup,
				ExitCode: 0,
			}},
			SuccessfulCommands: []profile.SetupCommand{{
				Run: "true",
				Why: "no-op setup",
			}},
			ReadinessEvidence: "setup observed effective executor",
			Source:            setuppreflight.SourceEnvironmentSetup,
			Provider:          opts.Task.Executor.CLI,
		}, nil
	})

	creatorManifest := `{"outputs":[{"ac_id":"AC1","path":"internal/foo/foo_test.go","kind":"go-test","purpose":"verify AC1","satisfies":"AC1 observable behavior","integration_point":"executor completes this skeleton before acceptance","implementation_required":true}],"no_skeletons":[]}`
	executorResult := `{"status":"completed","summary":"done","files_modified":["daemon-output.txt","internal/foo/foo_test.go"],"acceptance_criteria":[{"id":"AC1","status":"satisfied","evidence":["diff"],"notes":"done"}],"verification":[],"scope_expansions":[],"decisions":[],"risks":[]}`
	// Both fake backends distinguish skeleton and implementation prompts.
	codexBin := writeFakeCommand(t, "codex", `out=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --output-last-message)
      out="$2"
      shift 2
      ;;
    *)
      shift
      ;;
  esac
done
input="$(cat)"
case "$input" in
  *"acceptance skeleton creator"*)
    mkdir -p internal/foo
    printf 'package foo_test\n' > internal/foo/foo_test.go
    printf '%s\n' '`+creatorManifest+`' > "$out"
    ;;
  *)
    echo change > daemon-output.txt
    printf '%s\n' '`+executorResult+`' > "$out"
    printf '%s\n' '{"type":"turn.completed","usage":{}}'
    ;;
esac
`)
	claudeBin := writeFakeClaude(t, `creator=0
for arg in "$@"; do
  case "$arg" in
    *"Galley Acceptance Skeleton Manifest"*) creator=1 ;;
  esac
done
if [ "$creator" = "1" ]; then
  mkdir -p internal/foo
  # Always rewrite so reused worktrees still record a creator workspace change.
  printf 'package foo_test\n// requeue-creator-%s\n' "$(date +%s%N)" > internal/foo/foo_test.go
  printf '%s\n' '{"type":"result","result":"{\"outputs\":[{\"ac_id\":\"AC1\",\"path\":\"internal/foo/foo_test.go\",\"kind\":\"go-test\",\"purpose\":\"verify AC1\",\"satisfies\":\"AC1 observable behavior\",\"integration_point\":\"executor completes this skeleton before acceptance\",\"implementation_required\":true}],\"no_skeletons\":[]}"}'
  exit 0
fi
echo change > daemon-output.txt
printf '%s\n' '`+executorResult+`'
`)

	taskPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, taskPath, repo)
	loaded, err := task.Load(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Executor = task.Executor{Model: "task-model"}
	loaded.Preflight = &task.Preflight{AcceptanceSkeleton: &task.AcceptanceSkeletonConfig{Enabled: true}}
	if err := task.Save(taskPath, loaded); err != nil {
		t.Fatal(err)
	}

	opts := Options{
		Root:                   root,
		SystemPromptFile:       promptPath,
		JSONSchemaFile:         schemaPath,
		EnvironmentProfileFile: envPath,
		Once:                   true,
		MaxConcurrentTasks:     1,
		Supervisor:             "claude",
		ClaudeBin:              claudeBin,
		CodexBin:               codexBin,
	}
	if err := runTestDaemon(context.Background(), opts); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if len(setupCalls) != 1 {
		t.Fatalf("first run setup calls = %d, want 1", len(setupCalls))
	}
	wantFirst := observed{CLI: "codex", Model: "task-model", Effort: "minimal"}
	if setupCalls[0] != wantFirst {
		t.Fatalf("first setup effective = %#v, want %#v", setupCalls[0], wantFirst)
	}

	firstSetupMatches, err := filepath.Glob(filepath.Join(root, "runs", "*", "setup_result.json"))
	if err != nil || len(firstSetupMatches) != 1 {
		t.Fatalf("first setup_result.json glob = %v err %v", firstSetupMatches, err)
	}
	firstSetupData, err := os.ReadFile(firstSetupMatches[0])
	if err != nil {
		t.Fatal(err)
	}
	var firstSetup setuppreflight.Result
	if err := json.Unmarshal(firstSetupData, &firstSetup); err != nil {
		t.Fatal(err)
	}
	if firstSetup.ExecutorCLI != "codex" || firstSetup.ExecutorModel != "task-model" || firstSetup.ExecutorEffort != "minimal" {
		t.Fatalf("first setup identity = cli=%q model=%q effort=%q", firstSetup.ExecutorCLI, firstSetup.ExecutorModel, firstSetup.ExecutorEffort)
	}

	donePath := filepath.Join(root, "tasks", "done", "task.yaml")
	done, err := task.Load(donePath)
	if err != nil {
		t.Fatalf("first done task: %v", err)
	}
	if done.Executor.CLI != "" || done.Executor.Effort != "" || done.Executor.Model != "task-model" {
		t.Fatalf("authored executor must stay partial after first run, got %#v", done.Executor)
	}

	if err := os.WriteFile(envPath, []byte(envBody("claude", "env-model-v2", "xhigh")), 0o600); err != nil {
		t.Fatal(err)
	}
	requeued, err := task.Requeue(donePath, task.RequeueOptions{Root: root, Reason: "env defaults changed"})
	if err != nil {
		t.Fatalf("requeue: %v", err)
	}
	if requeued.Task.Executor.CLI != "" || requeued.Task.Executor.Effort != "" || requeued.Task.Executor.Model != "task-model" {
		t.Fatalf("requeued authored executor must stay partial, got %#v", requeued.Task.Executor)
	}

	if err := runTestDaemon(context.Background(), opts); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if len(setupCalls) != 2 {
		t.Fatalf("second run must re-invoke setup after identity change; setup calls = %d, want 2; calls=%#v", len(setupCalls), setupCalls)
	}
	wantSecond := observed{CLI: "claude", Model: "task-model", Effort: "xhigh"}
	if setupCalls[1] != wantSecond {
		t.Fatalf("second setup effective = %#v, want %#v", setupCalls[1], wantSecond)
	}

	plans, err := filepath.Glob(filepath.Join(root, "runs", "*", "preflight_creator_command_plan.json"))
	if err != nil || len(plans) == 0 {
		t.Fatalf("preflight_creator_command_plan.json glob = %v err %v", plans, err)
	}
	newestPlan := plans[0]
	newestMod := int64(0)
	for _, p := range plans {
		st, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if st.ModTime().UnixNano() >= newestMod {
			newestMod = st.ModTime().UnixNano()
			newestPlan = p
		}
	}
	planData, err := os.ReadFile(newestPlan)
	if err != nil {
		t.Fatal(err)
	}
	var plan struct {
		Argv []string `json:"argv"`
	}
	if err := json.Unmarshal(planData, &plan); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(plan.Argv, " ")
	if filepath.Base(plan.Argv[0]) != "claude" {
		t.Fatalf("second skeleton creator cli = %v, want claude", plan.Argv)
	}
	if !strings.Contains(joined, "task-model") {
		t.Fatalf("second skeleton creator missing task model: %v", plan.Argv)
	}
	if !strings.Contains(joined, "xhigh") {
		t.Fatalf("second skeleton creator missing new effort: %v", plan.Argv)
	}

	done2, err := task.Load(filepath.Join(root, "tasks", "done", filepath.Base(requeued.To)))
	if err != nil {
		entries, readErr := os.ReadDir(filepath.Join(root, "tasks", "done"))
		if readErr != nil || len(entries) == 0 {
			t.Fatalf("second done task: %v (scan err %v)", err, readErr)
		}
		done2, err = task.Load(filepath.Join(root, "tasks", "done", entries[0].Name()))
		if err != nil {
			t.Fatalf("second done task load: %v", err)
		}
	}
	if done2.Executor.CLI != "" || done2.Executor.Effort != "" || done2.Executor.Model != "task-model" {
		t.Fatalf("authored executor must stay partial after second run, got %#v", done2.Executor)
	}
	foundClaude := false
	for _, vc := range done2.Verification.Commands {
		if strings.Contains(vc.Cmd, "claude") {
			foundClaude = true
			break
		}
	}
	if !foundClaude {
		t.Fatalf("second implementation verification missing claude command: %#v", done2.Verification.Commands)
	}
}
