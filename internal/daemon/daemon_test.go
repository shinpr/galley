package daemon

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/shinpr/galley/internal/runartifact"
	"github.com/shinpr/galley/internal/task"
	"github.com/shinpr/galley/internal/workspace"
)

func TestRunOnceMovesTaskToDoneAndWritesRunEvidence(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	promptPath, schemaPath := writeDaemonPromptFiles(t)
	claudeBin := writeFakeClaude(t, "echo change > daemon-output.txt\necho '{\"status\":\"completed\",\"summary\":\"done\",\"files_modified\":[\"daemon-output.txt\"],\"acceptance_criteria\":[{\"id\":\"AC1\",\"status\":\"satisfied\",\"evidence\":[\"diff\"],\"notes\":\"done\"}],\"verification\":[],\"scope_expansions\":[],\"decisions\":[],\"risks\":[]}'\n")
	taskPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, taskPath, repo)
	setLoopBudget(t, taskPath, 2)

	err := runTestDaemon(context.Background(), Options{
		Root:               root,
		SystemPromptFile:   promptPath,
		JSONSchemaFile:     schemaPath,
		Once:               true,
		MaxConcurrentTasks: 1,
		Supervisor:         "claude",
		ClaudeBin:          claudeBin,
	})
	if err != nil {
		t.Fatal(err)
	}

	donePath := filepath.Join(root, "tasks", "done", "task.yaml")
	if _, err := os.Stat(donePath); err != nil {
		t.Fatalf("done task missing: %v", err)
	}
	doneTask, err := task.Load(donePath)
	if err != nil {
		t.Fatal(err)
	}
	if doneTask.Status != "accepted" {
		t.Fatalf("status got %q", doneTask.Status)
	}
	if doneTask.Scope.CWD != repo {
		t.Fatalf("scope cwd got %q, want source repo %q", doneTask.Scope.CWD, repo)
	}
	if len(doneTask.Attempts) != 1 || doneTask.Attempts[0].ClaudeStatus != "completed" {
		t.Fatalf("attempts got %#v", doneTask.Attempts)
	}
	if !strings.Contains(doneTask.Attempts[0].Summary, "workspace=") {
		t.Fatalf("attempt summary missing workspace: %q", doneTask.Attempts[0].Summary)
	}
	if len(doneTask.Verification.Commands) < 1 || doneTask.Verification.Commands[0].Status != "passed" {
		t.Fatalf("verification got %#v", doneTask.Verification.Commands)
	}
	assertGlobCount(t, filepath.Join(root, "runs", "*", "task.yaml"), 1)
	assertGlobCount(t, filepath.Join(root, "runs", "*", "workspace.json"), 1)
	assertGlobCount(t, filepath.Join(root, "runs", "*", "task.effective.yaml"), 1)
	assertGlobCount(t, filepath.Join(root, "runs", "*", "attempt-1", "command_plan.json"), 1)
	assertGlobCount(t, filepath.Join(root, "runs", "*", "attempt-1", "run_result.json"), 1)
	assertGlobCount(t, filepath.Join(root, "runs", "*", "attempt-1", runartifact.ExecutorResultFilename), 1)
	assertGlobCount(t, filepath.Join(root, "runs", "*", "attempt-1", "supervisor_verdict.json"), 1)
	assertGlobCount(t, filepath.Join(root, "runs", "*", "attempt-1", "git_status.json"), 1)
	assertGlobCount(t, filepath.Join(root, "runs", "*", "attempt-1", "diff.patch"), 1)
	assertGlobCount(t, filepath.Join(root, "runs", "*", "attempt-1", "claude.stdout.jsonl"), 1)
}

func TestRunOnceBuiltInAcceptanceSkeletonCreatorUpdatesTaskBeforeExecutor(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	promptPath, schemaPath := writeDaemonPromptFiles(t)
	claudeBin := writeFakeClaude(t, `creator=0
for arg in "$@"; do
  case "$arg" in
    *"Galley Acceptance Skeleton Manifest"*) creator=1 ;;
  esac
done
if [ "$creator" = "1" ]; then
  mkdir -p internal/foo
  printf 'package foo_test\n\n// TODO(galley-skeleton): implement AC1 assertion.\n' > internal/foo/foo_test.go
  printf '%s\n' '{"type":"result","result":"{\"outputs\":[{\"ac_id\":\"AC1\",\"path\":\"internal/foo/foo_test.go\",\"kind\":\"go-test\",\"purpose\":\"verify AC1\",\"satisfies\":\"AC1 observable behavior\",\"integration_point\":\"executor completes this skeleton before acceptance\",\"implementation_required\":true}],\"no_skeletons\":[]}"}'
  exit 0
fi
echo change > daemon-output.txt
echo '{"status":"completed","summary":"done","files_modified":["daemon-output.txt","internal/foo/foo_test.go"],"acceptance_criteria":[{"id":"AC1","status":"satisfied","evidence":["diff"],"notes":"done"}],"verification":[],"scope_expansions":[],"decisions":[],"risks":[]}'
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
	loaded.Preflight = &task.Preflight{AcceptanceSkeleton: &task.AcceptanceSkeletonConfig{Enabled: true}}
	if err := task.Save(taskPath, loaded); err != nil {
		t.Fatal(err)
	}

	err = runTestDaemon(context.Background(), Options{
		Root:               root,
		SystemPromptFile:   promptPath,
		JSONSchemaFile:     schemaPath,
		Once:               true,
		MaxConcurrentTasks: 1,
		Supervisor:         "claude",
		ClaudeBin:          claudeBin,
	})
	if err != nil {
		t.Fatal(err)
	}

	doneTask, err := task.Load(filepath.Join(root, "tasks", "done", "task.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	outputs := doneTask.Preflight.AcceptanceSkeleton.Outputs
	if len(outputs) != 1 || outputs[0].Path != "internal/foo/foo_test.go" || outputs[0].Satisfies == "" || outputs[0].IntegrationPoint == "" {
		t.Fatalf("generated outputs not written to task: %+v", outputs)
	}
	if !strings.Contains(doneTask.AcceptanceCriteria[0].Verification, "Acceptance skeleton:") ||
		!strings.Contains(doneTask.AcceptanceCriteria[0].Verification, "AC1 observable behavior") {
		t.Fatalf("AC verification not annotated:\n%s", doneTask.AcceptanceCriteria[0].Verification)
	}
	assertGlobCount(t, filepath.Join(root, "runs", "*", "preflight_creator_command_plan.json"), 1)
	assertGlobCount(t, filepath.Join(root, "runs", "*", "preflight_creator_manifest.json"), 1)
	assertGlobCount(t, filepath.Join(root, "runs", "*", "preflight_result.json"), 1)
}

func TestRunOnceUsesModelSupervisorProvider(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	promptPath, schemaPath := writeDaemonPromptFiles(t)
	claudeBin := writeFakeClaude(t, "echo change > daemon-output.txt\necho '{\"status\":\"completed\",\"summary\":\"done\",\"files_modified\":[\"daemon-output.txt\"],\"acceptance_criteria\":[{\"id\":\"AC1\",\"status\":\"satisfied\",\"evidence\":[\"diff\"],\"notes\":\"done\"}],\"verification\":[],\"scope_expansions\":[],\"decisions\":[],\"risks\":[]}'\n")
	codexBin := writeFakeCodexSupervisor(t, `{"status":"accepted","summary":"codex accepted","acceptance_gaps":[],"reviewed_files":["daemon-output.txt"],"acceptance_evidence":[{"ac_id":"AC1","evidence":["diff"]}],"quality_passes":[],"quality_gaps":[],"findings":[],"residual_risks":[],"discussion_items":[],"confidence":"high","next_work_order":""}`)
	taskPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, taskPath, repo)

	err := runTestDaemon(context.Background(), Options{
		Root:               root,
		SystemPromptFile:   promptPath,
		JSONSchemaFile:     schemaPath,
		Once:               true,
		MaxConcurrentTasks: 1,
		Supervisor:         "codex",
		ClaudeBin:          claudeBin,
		CodexBin:           codexBin,
	})
	if err != nil {
		t.Fatal(err)
	}
	doneTask, err := task.Load(filepath.Join(root, "tasks", "done", "task.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if doneTask.Attempts[0].SupervisorVerdict != "accepted" || !strings.Contains(doneTask.Attempts[0].Summary, "codex accepted") {
		t.Fatalf("attempts got %#v", doneTask.Attempts)
	}
	assertGlobCount(t, filepath.Join(root, "runs", "*", "attempt-1", "model_supervisor_verdict.json"), 1)
}

func TestRunOnceRecordsSupervisorTimeoutInTaskAttempt(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	promptPath, schemaPath := writeDaemonPromptFiles(t)
	claudeBin := writeFakeClaude(t, "echo change > daemon-output.txt\necho '{\"status\":\"completed\",\"summary\":\"done\",\"files_modified\":[\"daemon-output.txt\"],\"acceptance_criteria\":[{\"id\":\"AC1\",\"status\":\"satisfied\",\"evidence\":[\"diff\"],\"notes\":\"done\"}],\"verification\":[],\"scope_expansions\":[],\"decisions\":[],\"risks\":[]}'\n")
	codexBin := writeFakeCommand(t, "codex", "cat >/dev/null\nsleep 2\n")
	taskPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, taskPath, repo)
	// The timeout must let the fast executor reach its normal terminal while the
	// sleeping supervisor still exceeds it, so the recorded failure is the
	// supervisor timeout rather than an executor interruption.
	setTimeoutMS(t, taskPath, 500)

	err := runTestDaemon(context.Background(), Options{
		Root:               root,
		SystemPromptFile:   promptPath,
		JSONSchemaFile:     schemaPath,
		Once:               true,
		MaxConcurrentTasks: 1,
		Supervisor:         "codex",
		ClaudeBin:          claudeBin,
		CodexBin:           codexBin,
	})
	if err == nil {
		t.Fatal("expected supervisor timeout")
	}
	failedTask, err := task.Load(filepath.Join(root, "tasks", "failed", "task.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(failedTask.Attempts) == 0 {
		t.Fatal("expected failed attempt")
	}
	last := failedTask.Attempts[len(failedTask.Attempts)-1]
	if last.SupervisorVerdict != "timed_out" {
		t.Fatalf("supervisor verdict got %q", last.SupervisorVerdict)
	}
	if last.Error == nil {
		t.Fatalf("attempt error missing: %#v", last)
	}
	if last.Error.Phase != "supervisor" || last.Error.Kind != "timed_out" {
		t.Fatalf("attempt error got %#v", last.Error)
	}
	if !strings.Contains(last.Error.Message, "codex supervisor failed") || !strings.Contains(last.Error.Message, "timed out") {
		t.Fatalf("attempt error message got %q", last.Error.Message)
	}
	if last.Error.ArtifactDir == "" {
		t.Fatalf("attempt error artifact dir missing: %#v", last.Error)
	}
}

func TestRunOnceRetriesModelSupervisorWorkOrderUntilAccepted(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	promptPath, schemaPath := writeDaemonPromptFiles(t)
	claudeBin := writeFakeClaude(t, `if [ -f retry.marker ]; then
echo change > daemon-output.txt
echo '{"status":"completed","summary":"done","files_modified":["daemon-output.txt"],"acceptance_criteria":[{"id":"AC1","status":"satisfied","evidence":["diff"],"notes":"done"}],"verification":[],"scope_expansions":[],"decisions":[],"risks":[]}'
else
touch retry.marker
echo '{"status":"completed_with_risks","summary":"risky","files_modified":["retry.marker"],"acceptance_criteria":[{"id":"AC1","status":"satisfied","evidence":["diff"],"notes":"done"}],"verification":[],"scope_expansions":[],"decisions":[],"risks":[{"type":"partial_verification","detail":"needs retry","mitigation":"retry with corrective work order","needs_human_review":true}]}'
fi
`)
	taskPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, taskPath, repo)
	setLoopBudget(t, taskPath, 2)

	err := runTestDaemon(context.Background(), Options{
		Root:               root,
		SystemPromptFile:   promptPath,
		JSONSchemaFile:     schemaPath,
		Supervisor:         "claude",
		ClaudeBin:          claudeBin,
		Once:               true,
		MaxConcurrentTasks: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	doneTask, err := task.Load(filepath.Join(root, "tasks", "done", "task.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if doneTask.Status != "accepted" {
		t.Fatalf("status got %q", doneTask.Status)
	}
	if len(doneTask.Attempts) != 2 {
		t.Fatalf("attempts got %d", len(doneTask.Attempts))
	}
	if doneTask.Attempts[0].SupervisorVerdict != "needs_revision" || doneTask.Attempts[1].SupervisorVerdict != "accepted" {
		t.Fatalf("attempt verdicts got %#v", doneTask.Attempts)
	}
	if len(doneTask.RevisionRequests) != 1 || doneTask.RevisionRequests[0].Status != "addressed" {
		t.Fatalf("accepted task did not retain the addressed supervisor revision: %#v", doneTask.RevisionRequests)
	}
	assertGlobCount(t, filepath.Join(root, "runs", "*", "attempt-1", "supervisor_verdict.json"), 1)
	assertGlobCount(t, filepath.Join(root, "runs", "*", "attempt-2", "supervisor_verdict.json"), 1)
	effectiveTask := string(mustReadSingleGlob(t, filepath.Join(root, "runs", "*", "attempt-2", "task.effective.yaml")))
	if !strings.Contains(effectiveTask, "id: supervisor-attempt-1-finding-1") {
		t.Fatalf("attempt-2 effective task missing supervisor revision:\n%s", effectiveTask)
	}
	if strings.Contains(effectiveTask, "supervisor-attempt-1-gap") || strings.Contains(effectiveTask, "AC1 lacks independent verification") {
		t.Fatalf("attempt-2 effective task contains acceptance gap as a revision:\n%s", effectiveTask)
	}

	planData := mustReadSingleGlob(t, filepath.Join(root, "runs", "*", "attempt-2", "command_plan.json"))
	var plan struct {
		Argv []string `json:"argv"`
	}
	if err := json.Unmarshal(planData, &plan); err != nil {
		t.Fatalf("decode attempt-2 command plan: %v", err)
	}
	if len(plan.Argv) == 0 {
		t.Fatal("attempt-2 command plan argv is empty")
	}
	retryPrompt := plan.Argv[len(plan.Argv)-1]
	for _, want := range []string{
		"source=`supervisor`",
		"executor reported risks",
		"Resolve the reported risks and rerun verification.",
		"`revision:supervisor-attempt-1-finding-1`",
	} {
		if !strings.Contains(retryPrompt, want) {
			t.Fatalf("attempt-2 prompt missing %q:\n%s", want, retryPrompt)
		}
	}
	if strings.Contains(retryPrompt, "supervisor-attempt-1-gap") || strings.Contains(retryPrompt, "AC1 lacks independent verification") {
		t.Fatalf("attempt-2 prompt contains acceptance gap as a revision:\n%s", retryPrompt)
	}
}

func TestRunOncePersistsFinalSupervisorRevisionAcrossRequeue(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	promptPath, schemaPath := writeDaemonPromptFiles(t)
	claudeBin := writeFakeClaude(t, `echo change > daemon-output.txt
echo '{"status":"completed_with_risks","summary":"risky","files_modified":["daemon-output.txt"],"acceptance_criteria":[{"id":"AC1","status":"satisfied","evidence":["diff"],"notes":"done"}],"verification":[],"scope_expansions":[],"decisions":[],"risks":[{"type":"partial_verification","detail":"needs retry","mitigation":"retry with corrective work order","needs_human_review":true}]}'
`)
	taskPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, taskPath, repo)
	setLoopBudget(t, taskPath, 1)

	err := runTestDaemon(context.Background(), Options{
		Root:               root,
		SystemPromptFile:   promptPath,
		JSONSchemaFile:     schemaPath,
		Supervisor:         "claude",
		ClaudeBin:          claudeBin,
		Once:               true,
		MaxConcurrentTasks: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	failedPath := filepath.Join(root, "tasks", "failed", "task.yaml")
	failedTask, err := task.Load(failedPath)
	if err != nil {
		t.Fatal(err)
	}
	if failedTask.Status != "needs_supervisor_review" {
		t.Fatalf("status got %q", failedTask.Status)
	}
	assertSupervisorRevisionState(t, failedTask, "supervisor-attempt-1-finding-1", "executor reported risks", "Resolve the reported risks and rerun verification.")

	requeued, err := task.Requeue(failedPath, task.RequeueOptions{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	assertSupervisorRevisionState(t, requeued.Task, "supervisor-attempt-1-finding-1", "executor reported risks", "Resolve the reported risks and rerun verification.")

	acceptedClaudeBin := writeFakeClaude(t, `echo fixed >> daemon-output.txt
echo '{"status":"completed","summary":"fixed","files_modified":["daemon-output.txt"],"acceptance_criteria":[{"id":"AC1","status":"satisfied","evidence":["diff"],"notes":"done"},{"id":"revision:supervisor-attempt-1-finding-1","status":"satisfied","evidence":["diff"],"notes":"resolved"}],"verification":[],"scope_expansions":[],"decisions":[],"risks":[]}'
`)
	if err := runTestDaemon(context.Background(), Options{
		Root:               root,
		SystemPromptFile:   promptPath,
		JSONSchemaFile:     schemaPath,
		Supervisor:         "claude",
		ClaudeBin:          acceptedClaudeBin,
		Once:               true,
		MaxConcurrentTasks: 1,
	}); err != nil {
		t.Fatal(err)
	}
	doneTask, err := task.Load(filepath.Join(root, "tasks", "done", "task.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if doneTask.Status != "accepted" || len(doneTask.RevisionRequests) != 1 || doneTask.RevisionRequests[0].Status != "addressed" {
		t.Fatalf("accepted task did not retain addressed revision audit: status=%q revisions=%#v", doneTask.Status, doneTask.RevisionRequests)
	}
	for _, risk := range doneTask.Risks {
		if strings.HasPrefix(risk.ID, "requeue-supervisor-attempt-") {
			t.Fatalf("accepted task retained stale supervisor guidance: %#v", doneTask.Risks)
		}
	}
}

func TestRunOncePreservesExecutorDecisionsWithVerificationEvidence(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	promptPath, schemaPath := writeDaemonPromptFiles(t)
	claudeBin := writeFakeClaude(t, "echo change > daemon-output.txt\necho '{\"status\":\"completed\",\"summary\":\"chose a small reversible path\",\"files_modified\":[\"daemon-output.txt\"],\"acceptance_criteria\":[{\"id\":\"AC1\",\"status\":\"satisfied\",\"evidence\":[\"diff\"],\"notes\":\"done\"}],\"verification\":[],\"scope_expansions\":[],\"decisions\":[{\"question\":\"Which implementation should be used?\",\"chosen\":\"small reversible change\",\"rationale\":\"It satisfies AC1 with minimal blast radius.\",\"reversibility\":\"high\",\"needs_human_review\":true}],\"risks\":[]}'\n")
	taskPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, taskPath, repo)

	err := runTestDaemon(context.Background(), Options{
		Root:               root,
		SystemPromptFile:   promptPath,
		JSONSchemaFile:     schemaPath,
		Supervisor:         "claude",
		ClaudeBin:          claudeBin,
		Once:               true,
		MaxConcurrentTasks: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	doneTask, err := task.Load(filepath.Join(root, "tasks", "done", "task.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(doneTask.Decisions) != 1 {
		t.Fatalf("decisions got %#v", doneTask.Decisions)
	}
	if doneTask.Decisions[0].Chosen != "small reversible change" || !doneTask.Decisions[0].NeedsHumanReview {
		t.Fatalf("decision got %#v", doneTask.Decisions[0])
	}
}

func TestRunOnceOpenPRCommitsPushesAndUpdatesTask(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	runDaemonGit(t, t.TempDir(), "init", "--bare", remote)
	runDaemonGit(t, repo, "remote", "add", "origin", remote)
	promptPath, schemaPath := writeDaemonPromptFiles(t)
	claudeBin := writeFakeClaude(t, "echo change > daemon-output.txt\necho '{\"status\":\"completed\",\"summary\":\"done\",\"files_modified\":[\"daemon-output.txt\"],\"acceptance_criteria\":[{\"id\":\"AC1\",\"status\":\"satisfied\",\"evidence\":[\"diff\"],\"notes\":\"done\"}],\"verification\":[],\"scope_expansions\":[],\"decisions\":[],\"risks\":[]}'\n")
	ghBin := writeFakeCommand(t, "gh", "echo https://github.com/example/galley/pull/123\n")
	taskPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, taskPath, repo)

	err := runTestDaemon(context.Background(), Options{
		Root:               root,
		SystemPromptFile:   promptPath,
		JSONSchemaFile:     schemaPath,
		Supervisor:         "claude",
		ClaudeBin:          claudeBin,
		Once:               true,
		MaxConcurrentTasks: 1,
		OpenPR:             true,
		PRBase:             "main",
		GHBin:              ghBin,
	})
	if err != nil {
		t.Fatal(err)
	}

	doneTask, err := task.Load(filepath.Join(root, "tasks", "done", "task.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if doneTask.Status != "pr_opened" {
		t.Fatalf("status got %q", doneTask.Status)
	}
	if doneTask.PR.URL != "https://github.com/example/galley/pull/123" || doneTask.PR.Status != "open" {
		t.Fatalf("pr got %#v", doneTask.PR)
	}
	assertGlobCount(t, filepath.Join(root, "runs", "*", "pr_body.md"), 1)
	assertGlobCount(t, filepath.Join(root, "runs", "*", "git_commit_result.json"), 1)
	assertGlobCount(t, filepath.Join(root, "runs", "*", "git_push_result.json"), 1)
	assertGlobCount(t, filepath.Join(root, "runs", "*", "gh_pr_create_result.json"), 1)
}

// TestRunOnceOpenPRPersistsPRAuthorLogin exercises the success path for the
// PR author capture step that protects later /galley PR comment handling.
// The saved done task must carry the login on PR.AuthorLogin so the PR-author
// trust check can run later without re-fetching from GitHub.
func TestRunOnceOpenPRPersistsPRAuthorLogin(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	runDaemonGit(t, t.TempDir(), "init", "--bare", remote)
	runDaemonGit(t, repo, "remote", "add", "origin", remote)
	promptPath, schemaPath := writeDaemonPromptFiles(t)
	claudeBin := writeFakeClaude(t, "echo change > daemon-output.txt\necho '{\"status\":\"completed\",\"summary\":\"done\",\"files_modified\":[\"daemon-output.txt\"],\"acceptance_criteria\":[{\"id\":\"AC1\",\"status\":\"satisfied\",\"evidence\":[\"diff\"],\"notes\":\"done\"}],\"verification\":[],\"scope_expansions\":[],\"decisions\":[],\"risks\":[]}'\n")
	// Branched fake gh: pr create returns the URL; api repos/.../pulls/123
	// returns the PR author JSON payload that vcs.FetchPRAuthorLogin parses.
	ghBin := writeFakeCommand(t, "gh", `if [ "$1" = "pr" ] && [ "$2" = "create" ]; then
  echo https://github.com/example/galley/pull/321
elif [ "$1" = "api" ] && [ "$2" = "repos/example/galley/pulls/321" ]; then
  printf '%s\n' '{"user":{"login":"pr-author"}}'
else
  echo "unexpected gh args: $*" >&2
  exit 1
fi
`)
	taskPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, taskPath, repo)

	err := runTestDaemon(context.Background(), Options{
		Root:               root,
		SystemPromptFile:   promptPath,
		JSONSchemaFile:     schemaPath,
		Supervisor:         "claude",
		ClaudeBin:          claudeBin,
		Once:               true,
		MaxConcurrentTasks: 1,
		OpenPR:             true,
		PRBase:             "main",
		GHBin:              ghBin,
	})
	if err != nil {
		t.Fatal(err)
	}

	doneTask, err := task.Load(filepath.Join(root, "tasks", "done", "task.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if doneTask.PR.URL != "https://github.com/example/galley/pull/321" || doneTask.PR.Status != "open" {
		t.Fatalf("pr got %#v", doneTask.PR)
	}
	if doneTask.PR.AuthorLogin != "pr-author" {
		t.Fatalf("pr author login got %q want %q", doneTask.PR.AuthorLogin, "pr-author")
	}
	// Success path must not record the author-lookup risk that the failure
	// branch in finalizeAcceptedChange would otherwise append.
	for _, risk := range doneTask.Risks {
		if strings.HasPrefix(risk.ID, "pr-author-lookup-") {
			t.Fatalf("unexpected pr-author-lookup risk on success path: %#v", risk)
		}
	}
}

func TestRunOnceOpenPRUsesExecutorCommit(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	runDaemonGit(t, t.TempDir(), "init", "--bare", remote)
	runDaemonGit(t, repo, "remote", "add", "origin", remote)
	promptPath, schemaPath := writeDaemonPromptFiles(t)
	claudeBin := writeFakeClaude(t, "echo committed > daemon-output.txt\ngit add daemon-output.txt\ngit commit -m executor-commit\necho '{\"status\":\"completed\",\"summary\":\"done\",\"files_modified\":[\"daemon-output.txt\"],\"acceptance_criteria\":[{\"id\":\"AC1\",\"status\":\"satisfied\",\"evidence\":[\"committed diff\"],\"notes\":\"done\"}],\"verification\":[],\"scope_expansions\":[],\"decisions\":[],\"risks\":[]}'\n")
	ghBin := writeFakeCommand(t, "gh", "echo https://github.com/example/galley/pull/789\n")
	taskPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, taskPath, repo)

	err := runTestDaemon(context.Background(), Options{
		Root:               root,
		SystemPromptFile:   promptPath,
		JSONSchemaFile:     schemaPath,
		Supervisor:         "claude",
		ClaudeBin:          claudeBin,
		Once:               true,
		MaxConcurrentTasks: 1,
		OpenPR:             true,
		PRBase:             "main",
		GHBin:              ghBin,
	})
	if err != nil {
		t.Fatal(err)
	}

	doneTask, err := task.Load(filepath.Join(root, "tasks", "done", "task.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if doneTask.PR.URL != "https://github.com/example/galley/pull/789" {
		t.Fatalf("pr url got %q", doneTask.PR.URL)
	}
	assertGlobCount(t, filepath.Join(root, "runs", "*", "git_push_result.json"), 1)
	assertGlobCount(t, filepath.Join(root, "runs", "*", "gh_pr_create_result.json"), 1)
	assertGlobCount(t, filepath.Join(root, "runs", "*", "git_commit_result.json"), 0)
	worktreePath := taskWorktreePath(repo, doneTask.Worktree.Path)
	if got := strings.TrimSpace(string(mustCommandOutput(t, "git", "-C", worktreePath, "log", "--oneline", "-1"))); !strings.Contains(got, "executor-commit") {
		t.Fatalf("last commit got %q", got)
	}
}

func TestRunOnceOpenPRCommitsAcceptedDiffOutsideAllowedPaths(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	runDaemonGit(t, t.TempDir(), "init", "--bare", remote)
	runDaemonGit(t, repo, "remote", "add", "origin", remote)
	promptPath, schemaPath := writeDaemonPromptFiles(t)
	claudeBin := writeFakeClaude(t, "echo allowed > daemon-output.txt\necho expanded > scope-extra.txt\necho '{\"status\":\"completed\",\"summary\":\"done\",\"files_modified\":[\"daemon-output.txt\",\"scope-extra.txt\"],\"acceptance_criteria\":[{\"id\":\"AC1\",\"status\":\"satisfied\",\"evidence\":[\"diff\"],\"notes\":\"done\"}],\"verification\":[],\"scope_expansions\":[],\"decisions\":[],\"risks\":[]}'\n")
	ghBin := writeFakeCommand(t, "gh", "echo https://github.com/example/galley/pull/456\n")
	taskPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, taskPath, repo)
	loaded, err := task.Load(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Scope.AllowedPaths = []string{"daemon-output.txt"}
	if err := task.Save(taskPath, loaded); err != nil {
		t.Fatal(err)
	}

	err = runTestDaemon(context.Background(), Options{
		Root:               root,
		SystemPromptFile:   promptPath,
		JSONSchemaFile:     schemaPath,
		Supervisor:         "claude",
		ClaudeBin:          claudeBin,
		Once:               true,
		MaxConcurrentTasks: 1,
		OpenPR:             true,
		PRBase:             "main",
		GHBin:              ghBin,
	})
	if err != nil {
		t.Fatal(err)
	}

	doneTask, err := task.Load(filepath.Join(root, "tasks", "done", "task.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	worktreePath := taskWorktreePath(repo, doneTask.Worktree.Path)
	if got := strings.TrimSpace(string(mustCommandOutput(t, "git", "-C", worktreePath, "show", "HEAD:scope-extra.txt"))); got != "expanded" {
		t.Fatalf("scope-expanded file was not committed, got %q", got)
	}
	body := string(mustReadSingleGlob(t, filepath.Join(root, "runs", "*", "pr_body.md")))
	if !strings.Contains(body, "Scope expansion") || !strings.Contains(body, "scope-extra.txt") {
		t.Fatalf("PR body missing scope expansion discussion:\n%s", body)
	}
}

func TestRunOnceCopiesInputFileAndRemovesBeforeCommit(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	runDaemonGit(t, t.TempDir(), "init", "--bare", remote)
	runDaemonGit(t, repo, "remote", "add", "origin", remote)
	promptPath, schemaPath := writeDaemonPromptFiles(t)
	inputPath := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(inputPath, []byte("design note from plan\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	claudeBin := writeFakeClaude(t, "grep 'design note' docs/plan.md >/dev/null\necho change > daemon-output.txt\necho '{\"status\":\"completed\",\"summary\":\"used plan\",\"files_modified\":[\"daemon-output.txt\"],\"acceptance_criteria\":[{\"id\":\"AC1\",\"status\":\"satisfied\",\"evidence\":[\"diff\",\"plan read\"],\"notes\":\"done\"}],\"verification\":[],\"scope_expansions\":[],\"decisions\":[],\"risks\":[]}'\n")
	ghBin := writeFakeCommand(t, "gh", "echo https://github.com/example/galley/pull/456\n")
	taskPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, taskPath, repo)
	loaded, err := task.Load(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Files = []task.InputFile{{
		Source:      inputPath,
		Destination: "docs/plan.md",
		Description: "design plan",
		Commit:      false,
	}}
	if err := task.Save(taskPath, loaded); err != nil {
		t.Fatal(err)
	}

	err = runTestDaemon(context.Background(), Options{
		Root:               root,
		SystemPromptFile:   promptPath,
		JSONSchemaFile:     schemaPath,
		Supervisor:         "claude",
		ClaudeBin:          claudeBin,
		Once:               true,
		MaxConcurrentTasks: 1,
		OpenPR:             true,
		PRBase:             "main",
		GHBin:              ghBin,
	})
	if err != nil {
		t.Fatal(err)
	}

	doneTask, err := task.Load(filepath.Join(root, "tasks", "done", "task.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if doneTask.PR.URL != "https://github.com/example/galley/pull/456" {
		t.Fatalf("pr url got %q", doneTask.PR.URL)
	}
	worktreePath := taskWorktreePath(repo, doneTask.Worktree.Path)
	if _, err := os.Stat(filepath.Join(worktreePath, "docs", "plan.md")); !os.IsNotExist(err) {
		t.Fatalf("expected non-committed input file removed before commit, stat err=%v", err)
	}
	commitOutput, err := exec.Command("git", "-C", worktreePath, "show", "--name-only", "--format=", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	committedFiles := string(commitOutput)
	if strings.Contains(committedFiles, "docs/plan.md") {
		t.Fatalf("non-committed input file was committed:\n%s", committedFiles)
	}
	assertGlobCount(t, filepath.Join(root, "runs", "*", "input_files.json"), 1)
}

func TestRunOnceMovesInvalidTaskToFailed(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	taskPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(taskPath, []byte("id: broken\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := runTestDaemon(context.Background(), Options{Root: root, Once: true})
	if err == nil {
		t.Fatal("expected error")
	}
	if _, statErr := os.Stat(filepath.Join(root, "tasks", "failed", "task.yaml")); statErr != nil {
		t.Fatalf("failed task missing: %v", statErr)
	}
}

func TestRunOnceHardStopMovesTaskToFailed(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	promptPath, schemaPath := writeDaemonPromptFiles(t)
	claudeBin := writeFakeClaude(t, "echo '{\"status\":\"hard_stop\",\"summary\":\"blocked\",\"files_modified\":[],\"acceptance_criteria\":[],\"verification\":[],\"scope_expansions\":[],\"decisions\":[],\"risks\":[],\"hard_stop\":{\"reason\":\"missing secret\",\"attempted\":[],\"needed_to_continue\":[\"secret\"]}}'\n")
	taskPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, taskPath, repo)

	err := runTestDaemon(context.Background(), Options{
		Root:               root,
		SystemPromptFile:   promptPath,
		JSONSchemaFile:     schemaPath,
		Supervisor:         "claude",
		ClaudeBin:          claudeBin,
		Once:               true,
		MaxConcurrentTasks: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	failedTask, err := task.Load(filepath.Join(root, "tasks", "failed", "task.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if failedTask.Status != "failed" {
		t.Fatalf("status got %q", failedTask.Status)
	}
}

func TestRunOnceParseFailureNeedsSupervisorReview(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	promptPath, schemaPath := writeDaemonPromptFiles(t)
	// A real Claude run that finishes normally but emits non-JSON still ends its
	// stream with a success result event; only the embedded executor result is
	// unparseable. That normal terminal must reach Supervisor review rather than
	// be misclassified as an interruption.
	claudeBin := writeFakeClaude(t, `printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"result":"not-json","session_id":"sess-parse"}'`+"\n")
	taskPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, taskPath, repo)

	err := runTestDaemon(context.Background(), Options{
		Root:               root,
		SystemPromptFile:   promptPath,
		JSONSchemaFile:     schemaPath,
		Supervisor:         "claude",
		ClaudeBin:          claudeBin,
		Once:               true,
		MaxConcurrentTasks: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	failedTask, err := task.Load(filepath.Join(root, "tasks", "failed", "task.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if failedTask.Status != "needs_supervisor_review" {
		t.Fatalf("status got %q", failedTask.Status)
	}
	if len(failedTask.Risks) == 0 {
		t.Fatal("expected parse risk")
	}
}

func TestRunOnceCompletedWithRisksNeedsSupervisorReview(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	promptPath, schemaPath := writeDaemonPromptFiles(t)
	claudeBin := writeFakeClaude(t, "echo '{\"status\":\"completed_with_risks\",\"summary\":\"done\",\"files_modified\":[],\"acceptance_criteria\":[],\"verification\":[],\"scope_expansions\":[],\"decisions\":[],\"risks\":[{\"type\":\"partial_verification\",\"detail\":\"tests skipped\",\"mitigation\":\"raw logs saved\",\"needs_human_review\":true}]}'\n")
	taskPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, taskPath, repo)

	err := runTestDaemon(context.Background(), Options{
		Root:               root,
		SystemPromptFile:   promptPath,
		JSONSchemaFile:     schemaPath,
		Supervisor:         "claude",
		ClaudeBin:          claudeBin,
		Once:               true,
		MaxConcurrentTasks: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	failedTask, err := task.Load(filepath.Join(root, "tasks", "failed", "task.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if failedTask.Status != "needs_supervisor_review" {
		t.Fatalf("status got %q", failedTask.Status)
	}
	if len(failedTask.Risks) == 0 {
		t.Fatal("expected Claude risk copied into task")
	}
}

func TestRunOnceCompletedWithoutDiffNeedsSupervisorReview(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	promptPath, schemaPath := writeDaemonPromptFiles(t)
	claudeBin := writeFakeClaude(t, "echo '{\"status\":\"completed\",\"summary\":\"done\",\"files_modified\":[],\"acceptance_criteria\":[],\"verification\":[],\"scope_expansions\":[],\"decisions\":[],\"risks\":[]}'\n")
	taskPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, taskPath, repo)

	err := runTestDaemon(context.Background(), Options{
		Root:               root,
		SystemPromptFile:   promptPath,
		JSONSchemaFile:     schemaPath,
		Supervisor:         "claude",
		ClaudeBin:          claudeBin,
		Once:               true,
		MaxConcurrentTasks: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	failedTask, err := task.Load(filepath.Join(root, "tasks", "failed", "task.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if failedTask.Status != "needs_supervisor_review" {
		t.Fatalf("status got %q", failedTask.Status)
	}
	if len(failedTask.Risks) == 0 {
		t.Fatal("expected no-diff risk")
	}
}

func TestRunOnceStopsAfterConsecutiveNoDiffAttempts(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	promptPath, schemaPath := writeDaemonPromptFiles(t)
	claudeBin := writeFakeClaude(t, "echo '{\"status\":\"completed\",\"summary\":\"done\",\"files_modified\":[],\"acceptance_criteria\":[{\"id\":\"AC1\",\"status\":\"satisfied\",\"evidence\":[\"claimed\"],\"notes\":\"claimed\"}],\"verification\":[],\"scope_expansions\":[],\"decisions\":[],\"risks\":[]}'\n")
	taskPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, taskPath, repo)
	setLoopBudget(t, taskPath, 5)

	err := runTestDaemon(context.Background(), Options{
		Root:               root,
		SystemPromptFile:   promptPath,
		JSONSchemaFile:     schemaPath,
		Supervisor:         "claude",
		ClaudeBin:          claudeBin,
		Once:               true,
		MaxConcurrentTasks: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	failedTask, err := task.Load(filepath.Join(root, "tasks", "failed", "task.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if failedTask.Status != "needs_supervisor_review" {
		t.Fatalf("status got %q", failedTask.Status)
	}
	if len(failedTask.Attempts) != 2 {
		t.Fatalf("attempts got %d", len(failedTask.Attempts))
	}
	if len(failedTask.Risks) == 0 || !strings.Contains(failedTask.Risks[len(failedTask.Risks)-1].Detail, "no git diff") {
		t.Fatalf("progress risk missing: %#v", failedTask.Risks)
	}
	assertSupervisorRevisionState(t, failedTask, "supervisor-attempt-1-finding-1", "no repository diff was produced", "Make the required repository change and return valid structured JSON.")
}

func writeFakeClaude(t *testing.T, body string) string {
	t.Helper()
	return writeFakeCommand(t, "claude", `supervisor=0
for arg in "$@"; do
  if [ "$arg" = "--no-session-persistence" ]; then
    supervisor=1
  fi
done
if [ "$supervisor" = "1" ]; then
  request="$(cat)"
  revision_gap=
  if printf '%s' "$request" | grep -q 'supervisor-attempt-1-finding-1'; then
    revision_gap=',"revision:supervisor-attempt-1-finding-1"'
  fi
  if printf '%s' "$request" | grep -q '"status":"hard_stop"'; then
    printf '%s\n' '{"status":"hard_stop","summary":"executor reported hard_stop","acceptance_gaps":[],"reviewed_files":[],"acceptance_evidence":[],"quality_passes":[],"quality_gaps":[],"findings":[{"severity":"high","category":"execution","file":"","summary":"executor reported hard_stop","blocks_acceptance":true}],"residual_risks":[],"discussion_items":[],"confidence":"high","next_work_order":""}'
  elif printf '%s' "$request" | grep -q '"parse_error":"'; then
    printf '%s\n' '{"status":"needs_revision","summary":"executor result was invalid","acceptance_gaps":["AC1"'"$revision_gap"'],"reviewed_files":[],"acceptance_evidence":[],"quality_passes":[],"quality_gaps":[],"findings":[{"severity":"medium","category":"verification","file":"","summary":"executor result JSON is invalid","blocks_acceptance":true}],"residual_risks":[],"discussion_items":[],"confidence":"high","next_work_order":"Return valid structured JSON and preserve any useful workspace changes."}'
  elif printf '%s' "$request" | grep -q '"status":"completed_with_risks"'; then
    printf '%s\n' '{"status":"needs_revision","summary":"executor reported risks","acceptance_gaps":["AC1"'"$revision_gap"'],"reviewed_files":[],"acceptance_evidence":[],"quality_passes":[],"quality_gaps":[],"findings":[{"severity":"medium","category":"verification","file":"","summary":"executor reported risks","blocks_acceptance":true}],"residual_risks":[],"discussion_items":[],"confidence":"high","next_work_order":"Resolve the reported risks and rerun verification."}'
  elif printf '%s' "$request" | grep -q '"diff_dirty":false'; then
    printf '%s\n' '{"status":"needs_revision","summary":"no repository diff was produced","acceptance_gaps":["AC1"'"$revision_gap"'],"reviewed_files":[],"acceptance_evidence":[],"quality_passes":[],"quality_gaps":[],"findings":[{"severity":"medium","category":"acceptance","file":"","summary":"no repository diff was produced","blocks_acceptance":true}],"residual_risks":[],"discussion_items":[],"confidence":"high","next_work_order":"Make the required repository change and return valid structured JSON."}'
  else
    if printf '%s' "$request" | grep -q 'supervisor-attempt-1-finding-1'; then
      printf '%s\n' '{"status":"accepted","summary":"fake claude supervisor accepted","acceptance_gaps":[],"reviewed_files":["daemon-output.txt"],"acceptance_evidence":[{"ac_id":"AC1","evidence":["diff"]},{"ac_id":"revision:supervisor-attempt-1-finding-1","evidence":["resolved in the retry diff"]}],"quality_passes":[],"quality_gaps":[],"findings":[],"residual_risks":[],"discussion_items":[],"confidence":"high","next_work_order":""}'
    else
      printf '%s\n' '{"status":"accepted","summary":"fake claude supervisor accepted","acceptance_gaps":[],"reviewed_files":["daemon-output.txt"],"acceptance_evidence":[{"ac_id":"AC1","evidence":["diff"]}],"quality_passes":[],"quality_gaps":[],"findings":[],"residual_risks":[],"discussion_items":[],"confidence":"high","next_work_order":""}'
    fi
  fi
  exit 0
fi
`+body+`
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"session_id":"fake-claude-session"}'
`)
}

func prepareDonePRTask(t *testing.T, taskPath, repo, prStatus string) (task.Task, string) {
	t.Helper()
	loaded, err := task.Load(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := workspace.Prepare(context.Background(), repo, loaded.Worktree, workspace.Options{})
	if err != nil {
		t.Fatal(err)
	}
	loaded.Status = "pr_opened"
	loaded.PR.URL = "https://github.com/example/galley/pull/123"
	loaded.PR.Status = prStatus
	if err := task.Save(taskPath, loaded); err != nil {
		t.Fatal(err)
	}
	return loaded, prepared.CWD
}

func writeFakeCommand(t *testing.T, name, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("test uses POSIX shell fake executor binaries")
	}
	binDir := t.TempDir()
	commandPath := filepath.Join(binDir, name)
	if err := os.WriteFile(commandPath, []byte("#!/bin/sh\n"+body), 0o700); err != nil {
		t.Fatal(err)
	}
	return commandPath
}

func waitForFileOrDone(t *testing.T, path string, done <-chan error) {
	t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("processAvailable returned before %s appeared: %v", path, err)
			}
			t.Fatalf("processAvailable completed before %s appeared", path)
		case <-deadline.C:
			t.Fatalf("timed out waiting for %s", path)
		case <-ticker.C:
		}
	}
}

func writeFakeCodexSupervisor(t *testing.T, verdict string) string {
	t.Helper()
	return writeFakeCommand(t, "codex", `out=""
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
cat >/dev/null
printf '%s\n' '`+verdict+`' > "$out"
printf '%s\n' '{"event":"done"}'
`)
}

func initDaemonGitRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	runDaemonGit(t, repo, "init")
	runDaemonGit(t, repo, "config", "user.email", "test@example.com")
	runDaemonGit(t, repo, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runDaemonGit(t, repo, "add", "README.md")
	runDaemonGit(t, repo, "commit", "-m", "initial")
	return repo
}

func runDaemonGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, output)
	}
}

func mustCommandOutput(t *testing.T, name string, args ...string) []byte {
	t.Helper()
	output, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v failed: %v\n%s", name, args, err, output)
	}
	return output
}

func writeDaemonPromptFiles(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	promptPath := filepath.Join(dir, "prompt.md")
	schemaPath := filepath.Join(dir, "schema.json")
	if err := os.WriteFile(promptPath, []byte("system prompt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(schemaPath, []byte(`{"type":"object"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return promptPath, schemaPath
}

func writeDaemonTask(t *testing.T, path, repo string) {
	t.Helper()
	name := filepath.Base(path)
	name = name[:len(name)-len(filepath.Ext(name))]
	worktreePath := filepath.Join("..", "worktrees", name)
	body := `id: "task-daemon-test"
mode: "afk"
status: "queued"
goal: "Test daemon."
acceptance_criteria:
  - id: "AC1"
    text: "Runs."
    verification: "test -f daemon-output.txt"
    status: "pending"
scope:
  cwd: ` + strconv.Quote(repo) + `
  allowed_paths:
    - "."
  forbidden_paths: []
  permission: "edit"
execution_policy:
  loop_budget: 1
  timeout_ms: 5000
  afk_decision_policy: "choose-smallest-reversible"
  stop_on_destructive_operation: true
  stop_on_missing_secret: false
  stop_on_external_service_unavailable: false
worktree:
  enabled: true
  branch: "agent/` + name + `"
  path: ` + strconv.Quote(worktreePath) + `
supervisor:
  review_iterations: 0
executor:
  cli: "claude"
  model: "opus"
  effort: "high"
  prompt_profile: "codexized-claude-executor-v1"
  prompt_mode: "replace"
decisions: []
risks: []
attempts: []
verification:
  commands: []
pr:
  url: ""
  status: ""
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func taskWorktreePath(repo, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(repo, path)
}

func setLoopBudget(t *testing.T, path string, count int) {
	t.Helper()
	loaded, err := task.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	loaded.ExecutionPolicy.LoopBudget.Count = count
	loaded.ExecutionPolicy.LoopBudget.Set = true
	if err := task.Save(path, loaded); err != nil {
		t.Fatal(err)
	}
}

func setTimeoutMS(t *testing.T, path string, timeoutMS int) {
	t.Helper()
	loaded, err := task.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	loaded.ExecutionPolicy.TimeoutMS = timeoutMS
	if err := task.Save(path, loaded); err != nil {
		t.Fatal(err)
	}
}

func assertGlobCount(t *testing.T, pattern string, want int) {
	t.Helper()
	matches, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != want {
		t.Fatalf("%s matched %d files, want %d: %#v", pattern, len(matches), want, matches)
	}
}

func mustReadSingleGlob(t *testing.T, pattern string) []byte {
	t.Helper()
	matches, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("%s matched %d files, want 1: %#v", pattern, len(matches), matches)
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	return data
}
