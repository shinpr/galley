package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	setuppreflight "github.com/shinpr/galley/internal/preflight/setup"
	"github.com/shinpr/galley/internal/profile"
	"github.com/shinpr/galley/internal/runartifact"
	"github.com/shinpr/galley/internal/task"
)

func writeSetupEnvironmentProfile(t *testing.T, dir string, body string) string {
	t.Helper()
	path := filepath.Join(dir, "environment-local.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
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
	assertSetupVerificationRecorded(t, doneTask, "environment.yaml=unchanged")
}

// assertSetupVerificationRecorded pins that the task's verification history
// carries a passed <galley:setup> entry whose excerpt names wantExcerpt.
func assertSetupVerificationRecorded(t *testing.T, doneTask task.Task, wantExcerpt string) {
	t.Helper()
	for _, vc := range doneTask.Verification.Commands {
		if !strings.HasPrefix(vc.Cmd, "<galley:setup") {
			continue
		}
		if vc.Status != "passed" {
			t.Fatalf("setup verification status got %q, want passed", vc.Status)
		}
		if !strings.Contains(vc.OutputExcerpt, wantExcerpt) {
			t.Fatalf("setup excerpt missing %q: %q", wantExcerpt, vc.OutputExcerpt)
		}
		return
	}
	t.Fatalf("task verification history missing <galley:setup> entry: %#v", doneTask.Verification.Commands)
}

// TestSetupPreflightSetupFailedClassifiesAndWritesEvidence proves AC9: a setup
// failure fails the task as phase=setup and keeps the failure facts in setup_result.json.
func TestSetupPreflightSetupFailedClassifiesAndWritesEvidence(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	promptPath, schemaPath := writeDaemonPromptFiles(t)
	envPath := writeSetupEnvironmentProfile(t, t.TempDir(), `id: "fail"
cwd: `+workdirQuote(repo)+`
commands:
  test_unit: "true"
constraints:
  network: "approval_required"
  secrets_policy: "never_read_env_files"
  destructive_commands: "deny"
`)
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
	taskPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, taskPath, repo)

	if err := runTestDaemon(context.Background(), Options{
		Root:                   root,
		SystemPromptFile:       promptPath,
		JSONSchemaFile:         schemaPath,
		EnvironmentProfileFile: envPath,
		Once:                   true,
		MaxConcurrentTasks:     1,
		Supervisor:             "claude",
		ClaudeBin:              writeFakeClaude(t, "exit 1\n"),
	}); err == nil {
		t.Fatal("expected daemon Run to surface the setup failure")
	}

	failed, err := task.Load(filepath.Join(root, "tasks", "failed", "task.yaml"))
	if err != nil {
		t.Fatalf("failed task missing: %v", err)
	}
	if len(failed.Attempts) == 0 || failed.Attempts[len(failed.Attempts)-1].Error == nil {
		t.Fatalf("no attempt error recorded: %#v", failed.Attempts)
	}
	last := failed.Attempts[len(failed.Attempts)-1].Error
	if last.Phase != "setup" || last.Kind != "setup_failed" {
		t.Fatalf("phase/kind got phase=%q kind=%q, want phase=setup kind=setup_failed", last.Phase, last.Kind)
	}

	var saved setuppreflight.Result
	if err := json.Unmarshal(mustReadSingleGlob(t, filepath.Join(root, "runs", "*", runartifact.SetupResultFilename)), &saved); err != nil {
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
}

// workdirQuote returns a YAML-safe double-quoted string for a path; t.TempDir
// returns absolute paths so simple Go strconv.Quote is sufficient for tests.
func workdirQuote(s string) string {
	// JSON-quote-style escaping is YAML 1.2 compatible for double-quoted
	// scalars in our examples.
	b, _ := json.Marshal(s)
	return string(b)
}
