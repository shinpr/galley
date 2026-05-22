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

	"github.com/shinpr/galley/internal/galleyhome"
	"github.com/shinpr/galley/internal/queue"
	"github.com/shinpr/galley/internal/task"
	"github.com/shinpr/galley/internal/workspace"
)

func TestRunOnceMovesTaskToDoneAndWritesRunEvidence(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	promptPath, schemaPath := writeDaemonPromptFiles(t)
	claudeBin := writeFakeClaude(t, "echo change > daemon-output.txt\necho '{\"status\":\"completed\",\"summary\":\"done\",\"files_modified\":[\"daemon-output.txt\"],\"acceptance_criteria\":[{\"id\":\"AC1\",\"status\":\"satisfied\",\"evidence\":[\"diff\"],\"notes\":\"done\"}],\"verification\":[],\"decisions\":[],\"risks\":[]}'\n")
	taskPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, taskPath, repo)
	setLoopBudget(t, taskPath, 2)

	err := Run(context.Background(), Options{
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
	assertGlobCount(t, filepath.Join(root, "runs", "*", "attempt-1", executorResultFilename), 1)
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
echo '{"status":"completed","summary":"done","files_modified":["daemon-output.txt","internal/foo/foo_test.go"],"acceptance_criteria":[{"id":"AC1","status":"satisfied","evidence":["diff"],"notes":"done"}],"verification":[],"decisions":[],"risks":[]}'
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

	err = Run(context.Background(), Options{
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
	claudeBin := writeFakeClaude(t, "echo change > daemon-output.txt\necho '{\"status\":\"completed\",\"summary\":\"done\",\"files_modified\":[\"daemon-output.txt\"],\"acceptance_criteria\":[{\"id\":\"AC1\",\"status\":\"satisfied\",\"evidence\":[\"diff\"],\"notes\":\"done\"}],\"verification\":[],\"decisions\":[],\"risks\":[]}'\n")
	codexBin := writeFakeCodexSupervisor(t, `{"status":"accepted","summary":"codex accepted","acceptance_gaps":[],"reviewed_files":["daemon-output.txt"],"acceptance_evidence":[{"ac_id":"AC1","evidence":["diff"]}],"findings":[],"residual_risks":[],"discussion_items":[],"confidence":"high","next_work_order":""}`)
	taskPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, taskPath, repo)

	err := Run(context.Background(), Options{
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
	claudeBin := writeFakeClaude(t, "echo change > daemon-output.txt\necho '{\"status\":\"completed\",\"summary\":\"done\",\"files_modified\":[\"daemon-output.txt\"],\"acceptance_criteria\":[{\"id\":\"AC1\",\"status\":\"satisfied\",\"evidence\":[\"diff\"],\"notes\":\"done\"}],\"verification\":[],\"decisions\":[],\"risks\":[]}'\n")
	codexBin := writeFakeCommand(t, "codex", "cat >/dev/null\nsleep 2\n")
	taskPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, taskPath, repo)
	setTimeoutMS(t, taskPath, 50)

	err := Run(context.Background(), Options{
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
echo '{"status":"completed","summary":"done","files_modified":["daemon-output.txt"],"acceptance_criteria":[{"id":"AC1","status":"satisfied","evidence":["diff"],"notes":"done"}],"verification":[],"decisions":[],"risks":[]}'
else
touch retry.marker
echo '{"status":"completed_with_risks","summary":"risky","files_modified":["retry.marker"],"acceptance_criteria":[{"id":"AC1","status":"satisfied","evidence":["diff"],"notes":"done"}],"verification":[],"decisions":[],"risks":[{"type":"partial_verification","detail":"needs retry","mitigation":"retry with corrective work order","needs_human_review":true}]}'
fi
`)
	taskPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, taskPath, repo)
	setLoopBudget(t, taskPath, 2)

	err := Run(context.Background(), Options{
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
	assertGlobCount(t, filepath.Join(root, "runs", "*", "attempt-1", "supervisor_verdict.json"), 1)
	assertGlobCount(t, filepath.Join(root, "runs", "*", "attempt-2", "supervisor_verdict.json"), 1)
}

func TestRunOncePreservesExecutorDecisionsWithVerificationEvidence(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	promptPath, schemaPath := writeDaemonPromptFiles(t)
	claudeBin := writeFakeClaude(t, "echo change > daemon-output.txt\necho '{\"status\":\"completed\",\"summary\":\"chose a small reversible path\",\"files_modified\":[\"daemon-output.txt\"],\"acceptance_criteria\":[{\"id\":\"AC1\",\"status\":\"satisfied\",\"evidence\":[\"diff\"],\"notes\":\"done\"}],\"verification\":[],\"decisions\":[{\"question\":\"Which implementation should be used?\",\"chosen\":\"small reversible change\",\"rationale\":\"It satisfies AC1 with minimal blast radius.\",\"reversibility\":\"high\",\"needs_human_review\":true}],\"risks\":[]}'\n")
	taskPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, taskPath, repo)

	err := Run(context.Background(), Options{
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
	claudeBin := writeFakeClaude(t, "echo change > daemon-output.txt\necho '{\"status\":\"completed\",\"summary\":\"done\",\"files_modified\":[\"daemon-output.txt\"],\"acceptance_criteria\":[{\"id\":\"AC1\",\"status\":\"satisfied\",\"evidence\":[\"diff\"],\"notes\":\"done\"}],\"verification\":[],\"decisions\":[],\"risks\":[]}'\n")
	ghBin := writeFakeCommand(t, "gh", "echo https://github.com/example/galley/pull/123\n")
	taskPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, taskPath, repo)

	err := Run(context.Background(), Options{
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
// The fake `gh` differentiates between `gh pr create ...` (returns the new PR
// URL) and `gh api repos/{owner}/{repo}/pulls/{number}` (returns a JSON
// payload with the PR author's login). The saved done task must carry the
// login on PR.AuthorLogin so the PR-author trust check can run later without
// re-fetching from GitHub.
func TestRunOnceOpenPRPersistsPRAuthorLogin(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	runDaemonGit(t, t.TempDir(), "init", "--bare", remote)
	runDaemonGit(t, repo, "remote", "add", "origin", remote)
	promptPath, schemaPath := writeDaemonPromptFiles(t)
	claudeBin := writeFakeClaude(t, "echo change > daemon-output.txt\necho '{\"status\":\"completed\",\"summary\":\"done\",\"files_modified\":[\"daemon-output.txt\"],\"acceptance_criteria\":[{\"id\":\"AC1\",\"status\":\"satisfied\",\"evidence\":[\"diff\"],\"notes\":\"done\"}],\"verification\":[],\"decisions\":[],\"risks\":[]}'\n")
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

	err := Run(context.Background(), Options{
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
	claudeBin := writeFakeClaude(t, "echo committed > daemon-output.txt\ngit add daemon-output.txt\ngit commit -m executor-commit\necho '{\"status\":\"completed\",\"summary\":\"done\",\"files_modified\":[\"daemon-output.txt\"],\"acceptance_criteria\":[{\"id\":\"AC1\",\"status\":\"satisfied\",\"evidence\":[\"committed diff\"],\"notes\":\"done\"}],\"verification\":[],\"decisions\":[],\"risks\":[]}'\n")
	ghBin := writeFakeCommand(t, "gh", "echo https://github.com/example/galley/pull/789\n")
	taskPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, taskPath, repo)

	err := Run(context.Background(), Options{
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
	claudeBin := writeFakeClaude(t, "echo allowed > daemon-output.txt\necho expanded > scope-extra.txt\necho '{\"status\":\"completed\",\"summary\":\"done\",\"files_modified\":[\"daemon-output.txt\",\"scope-extra.txt\"],\"acceptance_criteria\":[{\"id\":\"AC1\",\"status\":\"satisfied\",\"evidence\":[\"diff\"],\"notes\":\"done\"}],\"verification\":[],\"decisions\":[],\"risks\":[]}'\n")
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

	err = Run(context.Background(), Options{
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
	claudeBin := writeFakeClaude(t, "grep 'design note' docs/plan.md >/dev/null\necho change > daemon-output.txt\necho '{\"status\":\"completed\",\"summary\":\"used plan\",\"files_modified\":[\"daemon-output.txt\"],\"acceptance_criteria\":[{\"id\":\"AC1\",\"status\":\"satisfied\",\"evidence\":[\"diff\",\"plan read\"],\"notes\":\"done\"}],\"verification\":[],\"decisions\":[],\"risks\":[]}'\n")
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

	err = Run(context.Background(), Options{
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

	err := Run(context.Background(), Options{Root: root, Once: true})
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
	claudeBin := writeFakeClaude(t, "echo '{\"status\":\"hard_stop\",\"summary\":\"blocked\",\"files_modified\":[],\"acceptance_criteria\":[],\"verification\":[],\"decisions\":[],\"risks\":[],\"hard_stop\":{\"reason\":\"missing secret\",\"attempted\":[],\"needed_to_continue\":[\"secret\"]}}'\n")
	taskPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, taskPath, repo)

	err := Run(context.Background(), Options{
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
	claudeBin := writeFakeClaude(t, "echo not-json\n")
	taskPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, taskPath, repo)

	err := Run(context.Background(), Options{
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
	claudeBin := writeFakeClaude(t, "echo '{\"status\":\"completed_with_risks\",\"summary\":\"done\",\"files_modified\":[],\"acceptance_criteria\":[],\"verification\":[],\"decisions\":[],\"risks\":[{\"type\":\"partial_verification\",\"detail\":\"tests skipped\",\"mitigation\":\"raw logs saved\",\"needs_human_review\":true}]}'\n")
	taskPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, taskPath, repo)

	err := Run(context.Background(), Options{
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
	claudeBin := writeFakeClaude(t, "echo '{\"status\":\"completed\",\"summary\":\"done\",\"files_modified\":[],\"acceptance_criteria\":[],\"verification\":[],\"decisions\":[],\"risks\":[]}'\n")
	taskPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, taskPath, repo)

	err := Run(context.Background(), Options{
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
	claudeBin := writeFakeClaude(t, "echo '{\"status\":\"completed\",\"summary\":\"done\",\"files_modified\":[],\"acceptance_criteria\":[{\"id\":\"AC1\",\"status\":\"satisfied\",\"evidence\":[\"claimed\"],\"notes\":\"claimed\"}],\"verification\":[],\"decisions\":[],\"risks\":[]}'\n")
	taskPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, taskPath, repo)
	setLoopBudget(t, taskPath, 5)

	err := Run(context.Background(), Options{
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
}

func TestRunOnceDrainsQueue(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo1 := initDaemonGitRepo(t)
	repo2 := initDaemonGitRepo(t)
	promptPath, schemaPath := writeDaemonPromptFiles(t)
	claudeBin := writeFakeClaude(t, "echo change > daemon-output.txt\necho '{\"status\":\"completed\",\"summary\":\"done\",\"files_modified\":[\"daemon-output.txt\"],\"acceptance_criteria\":[{\"id\":\"AC1\",\"status\":\"satisfied\",\"evidence\":[\"diff\"],\"notes\":\"done\"}],\"verification\":[],\"decisions\":[],\"risks\":[]}'\n")
	queueDir := filepath.Join(root, "tasks", "queued")
	if err := os.MkdirAll(queueDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, filepath.Join(queueDir, "task-1.yaml"), repo1)
	writeDaemonTask(t, filepath.Join(queueDir, "task-2.yaml"), repo2)

	err := Run(context.Background(), Options{
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
	assertGlobCount(t, filepath.Join(root, "tasks", "queued", "*.yaml"), 0)
	assertGlobCount(t, filepath.Join(root, "tasks", "done", "*.yaml"), 2)
}

func TestRunOnceContinuesAfterTaskFailure(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	promptPath, schemaPath := writeDaemonPromptFiles(t)
	claudeBin := writeFakeClaude(t, "echo change > daemon-output.txt\necho '{\"status\":\"completed\",\"summary\":\"done\",\"files_modified\":[\"daemon-output.txt\"],\"acceptance_criteria\":[{\"id\":\"AC1\",\"status\":\"satisfied\",\"evidence\":[\"diff\"],\"notes\":\"done\"}],\"verification\":[],\"decisions\":[],\"risks\":[]}'\n")
	queueDir := filepath.Join(root, "tasks", "queued")
	if err := os.MkdirAll(queueDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(queueDir, "bad.yaml"), []byte("id: broken\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, filepath.Join(queueDir, "good.yaml"), repo)

	err := Run(context.Background(), Options{
		Root:               root,
		SystemPromptFile:   promptPath,
		JSONSchemaFile:     schemaPath,
		Supervisor:         "claude",
		ClaudeBin:          claudeBin,
		Once:               true,
		MaxConcurrentTasks: 1,
	})
	if err == nil {
		t.Fatal("expected first task error")
	}
	assertGlobCount(t, filepath.Join(root, "tasks", "queued", "*.yaml"), 0)
	assertGlobCount(t, filepath.Join(root, "tasks", "failed", "*.yaml"), 1)
	assertGlobCount(t, filepath.Join(root, "tasks", "done", "*.yaml"), 1)
}

func TestRunOnceRecordsValidationErrorsInTaskAttempt(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	promptPath, schemaPath := writeDaemonPromptFiles(t)
	claudeBin := writeFakeClaude(t, "echo should-not-run\n")
	taskPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, taskPath, repo)
	loaded, err := task.Load(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Scope.CWD = ""
	if err := task.Save(taskPath, loaded); err != nil {
		t.Fatal(err)
	}

	err = Run(context.Background(), Options{
		Root:               root,
		SystemPromptFile:   promptPath,
		JSONSchemaFile:     schemaPath,
		Supervisor:         "claude",
		ClaudeBin:          claudeBin,
		Once:               true,
		MaxConcurrentTasks: 1,
	})
	if err == nil {
		t.Fatal("expected validation error")
	}
	failedTask, err := task.Load(filepath.Join(root, "tasks", "failed", "task.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(failedTask.Attempts) == 0 {
		t.Fatal("expected validation attempt")
	}
	last := failedTask.Attempts[len(failedTask.Attempts)-1]
	if last.SupervisorVerdict != "validation_failed" {
		t.Fatalf("supervisor verdict got %q", last.SupervisorVerdict)
	}
	if last.Error == nil {
		t.Fatalf("attempt error missing: %#v", last)
	}
	if last.Error.Phase != "validation" || last.Error.Kind != "validation_failed" {
		t.Fatalf("attempt error got %#v", last.Error)
	}
	if !strings.Contains(last.Error.Message, "task validation failed") || !strings.Contains(last.Error.Message, "scope.cwd") {
		t.Fatalf("attempt error message got %q", last.Error.Message)
	}
}

func TestProcessAvailableSkipsClaimConflict(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	queueDir := filepath.Join(root, "tasks", "queued")
	runningDir := filepath.Join(root, "tasks", "running")
	if err := os.MkdirAll(queueDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(runningDir, 0o755); err != nil {
		t.Fatal(err)
	}
	queuedPath := filepath.Join(queueDir, "task.yaml")
	runningPath := filepath.Join(runningDir, "task.yaml")
	if err := os.WriteFile(queuedPath, []byte("queued"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := task.Save(runningPath, task.Task{ID: "task", Status: "running", Scope: task.Scope{CWD: t.TempDir()}}); err != nil {
		t.Fatal(err)
	}

	processed, err := processAvailable(context.Background(), Options{Root: root, MaxConcurrentTasks: 1, ClaimTTL: time.Hour}.withDefaults())
	if err != nil {
		t.Fatal(err)
	}
	if processed != 0 {
		t.Fatalf("processed got %d", processed)
	}
}

func TestProcessAvailableSkipsConflictAndClaimsLaterTask(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	promptPath, schemaPath := writeDaemonPromptFiles(t)
	claudeBin := writeFakeClaude(t, "echo change > daemon-output.txt\necho '{\"status\":\"completed\",\"summary\":\"done\",\"files_modified\":[\"daemon-output.txt\"],\"acceptance_criteria\":[{\"id\":\"AC1\",\"status\":\"satisfied\",\"evidence\":[\"diff\"],\"notes\":\"done\"}],\"verification\":[],\"decisions\":[],\"risks\":[]}'\n")
	queueDir := filepath.Join(root, "tasks", "queued")
	runningDir := filepath.Join(root, "tasks", "running")
	if err := os.MkdirAll(queueDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(runningDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(queueDir, "a-conflict.yaml"), []byte("queued"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := task.Save(filepath.Join(runningDir, "a-conflict.yaml"), task.Task{ID: "a-conflict", Status: "running", Scope: task.Scope{CWD: t.TempDir()}}); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, filepath.Join(queueDir, "b-good.yaml"), repo)

	processed, err := processAvailable(context.Background(), Options{
		Root:               root,
		SystemPromptFile:   promptPath,
		JSONSchemaFile:     schemaPath,
		Supervisor:         "claude",
		ClaudeBin:          claudeBin,
		MaxConcurrentTasks: 1,
		ClaimTTL:           time.Hour,
	}.withDefaults())
	if err != nil {
		t.Fatal(err)
	}
	if processed != 1 {
		t.Fatalf("processed got %d", processed)
	}
	assertGlobCount(t, filepath.Join(root, "tasks", "done", "b-good.yaml"), 1)
	assertGlobCount(t, filepath.Join(root, "tasks", "queued", "a-conflict.yaml"), 1)
}

func TestProcessAvailableHonorsMaxConcurrentPerRepo(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	promptPath, schemaPath := writeDaemonPromptFiles(t)
	claudeBin := writeFakeClaude(t, "echo change > daemon-output.txt\necho '{\"status\":\"completed\",\"summary\":\"done\",\"files_modified\":[\"daemon-output.txt\"],\"acceptance_criteria\":[{\"id\":\"AC1\",\"status\":\"satisfied\",\"evidence\":[\"diff\"],\"notes\":\"done\"}],\"verification\":[],\"decisions\":[],\"risks\":[]}'\n")
	queueDir := filepath.Join(root, "tasks", "queued")
	runningDir := filepath.Join(root, "tasks", "running")
	if err := os.MkdirAll(queueDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(runningDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, filepath.Join(runningDir, "active.yaml"), repo)
	writeDaemonTask(t, filepath.Join(queueDir, "queued.yaml"), repo)

	processed, err := processAvailable(context.Background(), Options{
		Root:                 root,
		SystemPromptFile:     promptPath,
		JSONSchemaFile:       schemaPath,
		Supervisor:           "claude",
		ClaudeBin:            claudeBin,
		MaxConcurrentTasks:   2,
		MaxConcurrentPerRepo: 1,
		ClaimTTL:             time.Hour,
	}.withDefaults())
	if err != nil {
		t.Fatal(err)
	}
	if processed != 0 {
		t.Fatalf("processed got %d", processed)
	}
	assertGlobCount(t, filepath.Join(root, "tasks", "queued", "queued.yaml"), 1)
}

func TestProcessAvailableAllowsDifferentRepos(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo1 := initDaemonGitRepo(t)
	repo2 := initDaemonGitRepo(t)
	promptPath, schemaPath := writeDaemonPromptFiles(t)
	claudeBin := writeFakeClaude(t, "echo change > daemon-output.txt\necho '{\"status\":\"completed\",\"summary\":\"done\",\"files_modified\":[\"daemon-output.txt\"],\"acceptance_criteria\":[{\"id\":\"AC1\",\"status\":\"satisfied\",\"evidence\":[\"diff\"],\"notes\":\"done\"}],\"verification\":[],\"decisions\":[],\"risks\":[]}'\n")
	queueDir := filepath.Join(root, "tasks", "queued")
	runningDir := filepath.Join(root, "tasks", "running")
	if err := os.MkdirAll(queueDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(runningDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, filepath.Join(runningDir, "active.yaml"), repo1)
	writeDaemonTask(t, filepath.Join(queueDir, "queued.yaml"), repo2)

	processed, err := processAvailable(context.Background(), Options{
		Root:                 root,
		SystemPromptFile:     promptPath,
		JSONSchemaFile:       schemaPath,
		Supervisor:           "claude",
		ClaudeBin:            claudeBin,
		MaxConcurrentTasks:   2,
		MaxConcurrentPerRepo: 1,
		ClaimTTL:             time.Hour,
	}.withDefaults())
	if err != nil {
		t.Fatal(err)
	}
	if processed != 1 {
		t.Fatalf("processed got %d", processed)
	}
	assertGlobCount(t, filepath.Join(root, "tasks", "done", "queued.yaml"), 1)
}

func TestProcessAvailableDoesNotBlockOnMultipleClaimErrors(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	if err := queue.EnsureLayout(root); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a.yaml", "b.yaml"} {
		dirPath := filepath.Join(root, "tasks", "queued", name)
		if err := os.MkdirAll(dirPath, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	done := make(chan error, 1)
	go func() {
		_, err := processAvailable(context.Background(), Options{
			Root:               root,
			MaxConcurrentTasks: 1,
			ClaimTTL:           time.Hour,
		}.withDefaults())
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected claim error")
		}
	case <-time.After(time.Second):
		t.Fatal("processAvailable blocked on claim errors")
	}
}

func TestProcessAvailableLetsClaimedTaskFinishAfterShutdown(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	promptPath, schemaPath := writeDaemonPromptFiles(t)
	started := filepath.Join(t.TempDir(), "claude-started")
	claudeBin := writeFakeClaude(t, "touch "+started+"\nsleep 0.05\necho change > daemon-output.txt\necho '{\"status\":\"completed\",\"summary\":\"done\",\"files_modified\":[\"daemon-output.txt\"],\"acceptance_criteria\":[{\"id\":\"AC1\",\"status\":\"satisfied\",\"evidence\":[\"diff\"],\"notes\":\"done\"}],\"verification\":[],\"decisions\":[],\"risks\":[]}'\n")
	queueDir := filepath.Join(root, "tasks", "queued")
	if err := os.MkdirAll(queueDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, filepath.Join(queueDir, "task.yaml"), repo)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	processed, err := processAvailable(ctx, Options{
		Root:               root,
		SystemPromptFile:   promptPath,
		JSONSchemaFile:     schemaPath,
		Supervisor:         "claude",
		ClaudeBin:          claudeBin,
		MaxConcurrentTasks: 1,
		ClaimTTL:           time.Hour,
		ShutdownTimeout:    time.Second,
	}.withDefaults())
	if err == nil {
		t.Fatal("expected canceled context before claim")
	}
	if processed != 0 {
		t.Fatalf("processed got %d", processed)
	}

	ctx, cancel = context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := processAvailable(ctx, Options{
			Root:               root,
			SystemPromptFile:   promptPath,
			JSONSchemaFile:     schemaPath,
			Supervisor:         "claude",
			ClaudeBin:          claudeBin,
			MaxConcurrentTasks: 1,
			ClaimTTL:           time.Hour,
			ShutdownTimeout:    time.Second,
		}.withDefaults())
		done <- err
	}()
	waitForFile(t, started)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	assertGlobCount(t, filepath.Join(root, "tasks", "done", "task.yaml"), 1)
}

func TestShutdownStopsBeforeRetryAttempt(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	promptPath, schemaPath := writeDaemonPromptFiles(t)
	attemptLog := filepath.Join(t.TempDir(), "attempts.log")
	claudeBin := writeFakeClaude(t, "echo attempt >> "+attemptLog+"\nsleep 0.05\necho change >> daemon-output.txt\necho '{\"status\":\"completed\",\"summary\":\"done\",\"files_modified\":[\"daemon-output.txt\"],\"acceptance_criteria\":[{\"id\":\"AC1\",\"status\":\"satisfied\",\"evidence\":[\"diff\"],\"notes\":\"done\"}],\"verification\":[],\"decisions\":[],\"risks\":[]}'\n")
	codexBin := writeFakeCodexSupervisor(t, `{"status":"needs_revision","summary":"codex wants retry","acceptance_gaps":["retry"],"reviewed_files":["daemon-output.txt"],"acceptance_evidence":[],"findings":[{"severity":"medium","category":"acceptance","file":"daemon-output.txt","summary":"retry","blocks_acceptance":true}],"residual_risks":[],"discussion_items":[],"confidence":"high","next_work_order":"try again"}`)
	queueDir := filepath.Join(root, "tasks", "queued")
	if err := os.MkdirAll(queueDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, filepath.Join(queueDir, "task.yaml"), repo)
	setLoopBudget(t, filepath.Join(queueDir, "task.yaml"), 2)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := processAvailable(ctx, Options{
			Root:               root,
			SystemPromptFile:   promptPath,
			JSONSchemaFile:     schemaPath,
			ClaudeBin:          claudeBin,
			MaxConcurrentTasks: 1,
			ClaimTTL:           time.Hour,
			ShutdownTimeout:    5 * time.Second,
			Supervisor:         "codex",
			CodexBin:           codexBin,
		}.withDefaults())
		done <- err
	}()
	waitForFile(t, attemptLog)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(attemptLog)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(data), "attempt"); got != 1 {
		t.Fatalf("attempt count got %d, log=%q", got, string(data))
	}
	failedTask, err := task.Load(filepath.Join(root, "tasks", "failed", "task.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if failedTask.Status != "needs_supervisor_review" {
		t.Fatalf("status got %q", failedTask.Status)
	}
	if len(failedTask.Risks) == 0 || !strings.Contains(failedTask.Risks[len(failedTask.Risks)-1].ID, "shutdown-") {
		t.Fatalf("shutdown risk missing: %#v", failedTask.Risks)
	}
}

func TestRunOnceStopsWhenOnlyClaimConflictsRemain(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	queueDir := filepath.Join(root, "tasks", "queued")
	runningDir := filepath.Join(root, "tasks", "running")
	if err := os.MkdirAll(queueDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(runningDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(queueDir, "task.yaml"), []byte("queued"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := task.Save(filepath.Join(runningDir, "task.yaml"), task.Task{ID: "task", Status: "running", Scope: task.Scope{CWD: t.TempDir()}}); err != nil {
		t.Fatal(err)
	}

	err := Run(context.Background(), Options{
		Root:               root,
		Once:               true,
		MaxConcurrentTasks: 1,
		ClaimTTL:           time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertGlobCount(t, filepath.Join(root, "tasks", "queued", "*.yaml"), 1)
	assertGlobCount(t, filepath.Join(root, "tasks", "running", "*.yaml"), 1)
}

func TestHeartbeatKeepsRunningTaskFresh(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	if err := queue.EnsureLayout(root); err != nil {
		t.Fatal(err)
	}
	runningPath := filepath.Join(root, "tasks", "running", "task.yaml")
	writeDaemonTask(t, runningPath, repo)
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(runningPath, old, old); err != nil {
		t.Fatal(err)
	}
	stop := startHeartbeat(context.Background(), runningPath, 10*time.Millisecond)
	time.Sleep(30 * time.Millisecond)
	stop()

	if err := queue.RecoverStaleClaims(root, time.Hour, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(runningPath); err != nil {
		t.Fatalf("running task should remain fresh: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "tasks", "queued", "task.yaml")); !os.IsNotExist(err) {
		t.Fatalf("task should not be requeued, err=%v", err)
	}
}

func TestCleanupWorktreesKeepsOpenPRWorktree(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	if err := queue.EnsureLayout(root); err != nil {
		t.Fatal(err)
	}
	taskPath := filepath.Join(root, "tasks", "done", "task.yaml")
	writeDaemonTask(t, taskPath, repo)
	doneTask, worktreePath := prepareDonePRTask(t, taskPath, repo, "open")
	ghBin := writeFakeCommand(t, "gh", "echo '{\"state\":\"open\",\"merged\":false}'\n")

	if err := cleanupWorktrees(context.Background(), Options{Root: root, GHBin: ghBin}.withDefaults()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(worktreePath); err != nil {
		t.Fatalf("open PR worktree should remain: %v", err)
	}
	reloaded, err := task.Load(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.PR.Status != doneTask.PR.Status || len(reloaded.Attempts) != len(doneTask.Attempts) {
		t.Fatalf("open PR task should not be updated: %#v", reloaded.PR)
	}
}

func TestCleanupWorktreesRemovesCleanMergedPRWorktree(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	if err := queue.EnsureLayout(root); err != nil {
		t.Fatal(err)
	}
	taskPath := filepath.Join(root, "tasks", "done", "task.yaml")
	writeDaemonTask(t, taskPath, repo)
	_, worktreePath := prepareDonePRTask(t, taskPath, repo, "open")
	ghBin := writeFakeCommand(t, "gh", "echo '{\"state\":\"closed\",\"merged\":true}'\n")

	if err := cleanupWorktrees(context.Background(), Options{Root: root, GHBin: ghBin}.withDefaults()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(worktreePath); !os.IsNotExist(err) {
		t.Fatalf("merged PR worktree should be removed, err=%v", err)
	}
	reloaded, err := task.Load(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.PR.Status != "merged" {
		t.Fatalf("pr status got %q", reloaded.PR.Status)
	}
	if reloaded.Status != "merged" {
		t.Fatalf("task status got %q", reloaded.Status)
	}
	if len(reloaded.Attempts) == 0 || reloaded.Attempts[len(reloaded.Attempts)-1].SupervisorVerdict != "cleanup" {
		t.Fatalf("cleanup attempt missing: %#v", reloaded.Attempts)
	}
}

func TestCleanupWorktreesRemovesDirtyClosedPRWorktree(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	if err := queue.EnsureLayout(root); err != nil {
		t.Fatal(err)
	}
	taskPath := filepath.Join(root, "tasks", "done", "task.yaml")
	writeDaemonTask(t, taskPath, repo)
	_, worktreePath := prepareDonePRTask(t, taskPath, repo, "open")
	if err := os.WriteFile(filepath.Join(worktreePath, "dirty.txt"), []byte("dirty\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ghBin := writeFakeCommand(t, "gh", "echo '{\"state\":\"closed\",\"merged\":false}'\n")

	if err := cleanupWorktrees(context.Background(), Options{Root: root, GHBin: ghBin}.withDefaults()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(worktreePath); !os.IsNotExist(err) {
		t.Fatalf("dirty worktree should be removed, err=%v", err)
	}
	reloaded, err := task.Load(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.PR.Status != "closed" {
		t.Fatalf("pr status got %q", reloaded.PR.Status)
	}
	if reloaded.Status != "closed" {
		t.Fatalf("task status got %q", reloaded.Status)
	}
	if len(reloaded.Attempts) == 0 || reloaded.Attempts[len(reloaded.Attempts)-1].SupervisorVerdict != "cleanup" {
		t.Fatalf("cleanup attempt missing: %#v", reloaded.Attempts)
	}
}

// TestRunOnceBranchesNewWorktreeFromEnvironmentPRBaseOriginRef covers AC2 +
// AC4 case (1): when the environment profile's pr.base resolves to an
// origin/<base> ref, the new task worktree is branched from that ref's SHA
// rather than the source repo's current HEAD. This also exercises the order
// requirement: profile resolution must happen before workspace.Prepare.
func TestRunOnceBranchesNewWorktreeFromEnvironmentPRBaseOriginRef(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	// Wire a real bare origin remote and publish feature-base at the initial
	// commit. The daemon's pre-resolve `git fetch origin feature-base` must
	// succeed against this remote so origin/feature-base remains the chosen
	// start-point. Advance source HEAD so origin/feature-base SHA differs
	// from the source repo HEAD SHA.
	remote := filepath.Join(t.TempDir(), "remote.git")
	runDaemonGit(t, t.TempDir(), "init", "--bare", remote)
	runDaemonGit(t, repo, "remote", "add", "origin", remote)
	baseSHA := strings.TrimSpace(string(mustCommandOutput(t, "git", "-C", repo, "rev-parse", "HEAD")))
	runDaemonGit(t, repo, "push", "origin", "HEAD:refs/heads/feature-base")
	if err := os.WriteFile(filepath.Join(repo, "advance.txt"), []byte("advance\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runDaemonGit(t, repo, "add", "advance.txt")
	runDaemonGit(t, repo, "commit", "-m", "advance")
	headSHA := strings.TrimSpace(string(mustCommandOutput(t, "git", "-C", repo, "rev-parse", "HEAD")))
	if baseSHA == headSHA {
		t.Fatal("setup failed: baseSHA and headSHA should differ")
	}
	writeRepoEnvironmentProfile(t, root, repo, "feature-base")

	promptPath, schemaPath := writeDaemonPromptFiles(t)
	claudeBin := writeFakeClaude(t, "echo change > daemon-output.txt\necho '{\"status\":\"completed\",\"summary\":\"done\",\"files_modified\":[\"daemon-output.txt\"],\"acceptance_criteria\":[{\"id\":\"AC1\",\"status\":\"satisfied\",\"evidence\":[\"diff\"],\"notes\":\"done\"}],\"verification\":[],\"decisions\":[],\"risks\":[]}'\n")
	taskPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, taskPath, repo)

	if err := Run(context.Background(), Options{
		Root:               root,
		SystemPromptFile:   promptPath,
		JSONSchemaFile:     schemaPath,
		Supervisor:         "claude",
		ClaudeBin:          claudeBin,
		Once:               true,
		MaxConcurrentTasks: 1,
	}); err != nil {
		t.Fatal(err)
	}
	doneTask, err := task.Load(filepath.Join(root, "tasks", "done", "task.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	worktreePath := taskWorktreePath(repo, doneTask.Worktree.Path)
	// The fake claude does not commit, so the new branch HEAD equals the
	// start-point ref's SHA. If profile resolution were skipped or ordered
	// after Prepare, the worktree HEAD would equal the source repo HEAD
	// (headSHA) instead.
	got := strings.TrimSpace(string(mustCommandOutput(t, "git", "-C", worktreePath, "rev-parse", "HEAD")))
	if got != baseSHA {
		t.Fatalf("worktree HEAD got %q, want origin/feature-base SHA %q (source HEAD=%q)", got, baseSHA, headSHA)
	}
	// AC7: profiles.json must still be written into the run directory.
	matches, err := filepath.Glob(filepath.Join(root, "runs", "*", "profiles.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected 1 profiles.json, got %d", len(matches))
	}
	var payload struct {
		Bundle struct {
			Environment *struct {
				PR struct {
					Base string `json:"base,omitempty"`
				} `json:"pr"`
			} `json:"environment,omitempty"`
		} `json:"bundle"`
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("decode profiles.json: %v", err)
	}
	if payload.Bundle.Environment == nil || payload.Bundle.Environment.PR.Base != "feature-base" {
		t.Fatalf("profiles.json bundle.environment.pr.base got %#v", payload.Bundle)
	}
}

// TestRunOnceBranchesNewWorktreeFromLocalHeadsFallback covers AC4 case (2):
// when origin/<base> is missing but refs/heads/<base> exists, the local
// branch is used as the start-point.
func TestRunOnceBranchesNewWorktreeFromLocalHeadsFallback(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	baseSHA := strings.TrimSpace(string(mustCommandOutput(t, "git", "-C", repo, "rev-parse", "HEAD")))
	runDaemonGit(t, repo, "branch", "feature-local")
	// Advance master/main so HEAD differs from feature-local tip.
	if err := os.WriteFile(filepath.Join(repo, "advance.txt"), []byte("advance\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runDaemonGit(t, repo, "add", "advance.txt")
	runDaemonGit(t, repo, "commit", "-m", "advance")
	headSHA := strings.TrimSpace(string(mustCommandOutput(t, "git", "-C", repo, "rev-parse", "HEAD")))
	if baseSHA == headSHA {
		t.Fatal("setup failed: baseSHA and headSHA should differ")
	}
	writeRepoEnvironmentProfile(t, root, repo, "feature-local")

	promptPath, schemaPath := writeDaemonPromptFiles(t)
	claudeBin := writeFakeClaude(t, "echo change > daemon-output.txt\necho '{\"status\":\"completed\",\"summary\":\"done\",\"files_modified\":[\"daemon-output.txt\"],\"acceptance_criteria\":[{\"id\":\"AC1\",\"status\":\"satisfied\",\"evidence\":[\"diff\"],\"notes\":\"done\"}],\"verification\":[],\"decisions\":[],\"risks\":[]}'\n")
	taskPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, taskPath, repo)

	if err := Run(context.Background(), Options{
		Root:               root,
		SystemPromptFile:   promptPath,
		JSONSchemaFile:     schemaPath,
		Supervisor:         "claude",
		ClaudeBin:          claudeBin,
		Once:               true,
		MaxConcurrentTasks: 1,
	}); err != nil {
		t.Fatal(err)
	}
	doneTask, err := task.Load(filepath.Join(root, "tasks", "done", "task.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	worktreePath := taskWorktreePath(repo, doneTask.Worktree.Path)
	got := strings.TrimSpace(string(mustCommandOutput(t, "git", "-C", worktreePath, "rev-parse", "HEAD")))
	if got != baseSHA {
		t.Fatalf("worktree HEAD got %q, want refs/heads/feature-local SHA %q", got, baseSHA)
	}
}

// TestRunOnceFailsWhenPRBaseRefMissing covers AC4 case (3): when neither
// origin/<base> nor refs/heads/<base> exists, the daemon must fail the
// claimed task with a descriptive error and record the attempt as
// phase=workspace.
func TestRunOnceFailsWhenPRBaseRefMissing(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	writeRepoEnvironmentProfile(t, root, repo, "does-not-exist")

	promptPath, schemaPath := writeDaemonPromptFiles(t)
	claudeBin := writeFakeClaude(t, "echo should-not-run\n")
	taskPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, taskPath, repo)

	err := Run(context.Background(), Options{
		Root:               root,
		SystemPromptFile:   promptPath,
		JSONSchemaFile:     schemaPath,
		Supervisor:         "claude",
		ClaudeBin:          claudeBin,
		Once:               true,
		MaxConcurrentTasks: 1,
	})
	if err == nil {
		t.Fatal("expected pr.base resolution failure")
	}
	failedTask, err := task.Load(filepath.Join(root, "tasks", "failed", "task.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(failedTask.Attempts) == 0 {
		t.Fatalf("expected a workspace failure attempt: %#v", failedTask)
	}
	last := failedTask.Attempts[len(failedTask.Attempts)-1]
	if last.Error == nil || last.Error.Phase != "workspace" {
		t.Fatalf("attempt error got %#v", last.Error)
	}
	if !strings.Contains(last.Error.Message, "refs/remotes/origin/does-not-exist") || !strings.Contains(last.Error.Message, "refs/heads/does-not-exist") {
		t.Fatalf("attempt error message missing both attempted refs: %q", last.Error.Message)
	}
}

// TestRunOnceFailsWhenStaleOriginRefAndFetchFails covers the tightened
// PR-review behavior: when the source repository has an origin remote, a
// stale refs/remotes/origin/<pr.base> cached locally, and `git fetch origin
// <pr.base>` cannot succeed (here, the configured origin URL is unreachable),
// the daemon must refuse to use the stale remote-tracking ref and instead
// fail the claimed task in the workspace phase. This prevents a stale local
// origin/<base> from silently anchoring a new task branch behind the actual
// remote tip when the daemon cannot confirm freshness.
func TestRunOnceFailsWhenStaleOriginRefAndFetchFails(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	// Wire origin to a bogus URL so `git fetch origin feature-stale` fails.
	bogusRemote := filepath.Join(t.TempDir(), "does-not-exist.git")
	runDaemonGit(t, repo, "remote", "add", "origin", bogusRemote)
	// Pre-create a stale refs/remotes/origin/feature-stale: if the daemon
	// did not refuse on fetch failure, this stale ref would still be
	// resolved as the start-point.
	staleSHA := strings.TrimSpace(string(mustCommandOutput(t, "git", "-C", repo, "rev-parse", "HEAD")))
	runDaemonGit(t, repo, "update-ref", "refs/remotes/origin/feature-stale", staleSHA)

	writeRepoEnvironmentProfile(t, root, repo, "feature-stale")

	promptPath, schemaPath := writeDaemonPromptFiles(t)
	claudeBin := writeFakeClaude(t, "echo should-not-run\n")
	taskPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, taskPath, repo)

	err := Run(context.Background(), Options{
		Root:               root,
		SystemPromptFile:   promptPath,
		JSONSchemaFile:     schemaPath,
		Supervisor:         "claude",
		ClaudeBin:          claudeBin,
		Once:               true,
		MaxConcurrentTasks: 1,
	})
	if err == nil {
		t.Fatal("expected workspace failure when origin fetch fails and stale origin/<base> exists")
	}
	failedTask, err := task.Load(filepath.Join(root, "tasks", "failed", "task.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(failedTask.Attempts) == 0 {
		t.Fatalf("expected a workspace failure attempt: %#v", failedTask)
	}
	last := failedTask.Attempts[len(failedTask.Attempts)-1]
	if last.Error == nil || last.Error.Phase != "workspace" {
		t.Fatalf("attempt error got %#v", last.Error)
	}
	// The error must surface the source repo path, the pr.base value, and
	// the failed fetch operation so `galley task show` exposes the reason.
	for _, want := range []string{repo, "feature-stale", "fetch", "refs/remotes/origin/feature-stale"} {
		if !strings.Contains(last.Error.Message, want) {
			t.Fatalf("attempt error message missing %q: %q", want, last.Error.Message)
		}
	}
	// No worktree must have been created from the stale ref.
	doneTask := filepath.Join(root, "tasks", "done", "task.yaml")
	if _, statErr := os.Stat(doneTask); statErr == nil {
		t.Fatalf("expected no done task, but found %s", doneTask)
	}
	// The stale local ref must remain untouched (the fetch failed, so no
	// refresh could have updated it). This documents that we did not
	// silently rewrite the stale ref while refusing to use it.
	stillStale := strings.TrimSpace(string(mustCommandOutput(t, "git", "-C", repo, "rev-parse", "refs/remotes/origin/feature-stale")))
	if stillStale != staleSHA {
		t.Fatalf("stale origin/feature-stale unexpectedly changed; got %q want %q", stillStale, staleSHA)
	}
}

// TestRunOnceRefreshesStaleOriginRefBeforeWorktreeCreation covers the
// PR-review revision request: when the source repository has an origin remote
// and a stale refs/remotes/origin/<pr.base> cached locally, the daemon must
// fetch origin <pr.base> before resolving the start-point so the new task
// branch starts from the latest remote tip rather than the stale local copy.
func TestRunOnceRefreshesStaleOriginRefBeforeWorktreeCreation(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	// Bare upstream remote and origin wiring on the source repo.
	remote := filepath.Join(t.TempDir(), "remote.git")
	runDaemonGit(t, t.TempDir(), "init", "--bare", remote)
	runDaemonGit(t, repo, "remote", "add", "origin", remote)
	// Seed the upstream feature-stale branch at SHA_A from the source repo.
	runDaemonGit(t, repo, "push", "origin", "HEAD:refs/heads/feature-stale")
	shaA := strings.TrimSpace(string(mustCommandOutput(t, "git", "-C", repo, "rev-parse", "HEAD")))
	// Pin the local remote-tracking ref at SHA_A so the cached origin/feature-stale
	// is genuinely stale once the upstream advances below.
	runDaemonGit(t, repo, "update-ref", "refs/remotes/origin/feature-stale", shaA)
	// Advance feature-stale on the upstream via a separate publisher clone so
	// the remote tip moves to SHA_B without touching the source repo.
	publisher := filepath.Join(t.TempDir(), "publisher")
	runDaemonGit(t, t.TempDir(), "clone", remote, publisher)
	runDaemonGit(t, publisher, "config", "user.email", "test@example.com")
	runDaemonGit(t, publisher, "config", "user.name", "Test User")
	runDaemonGit(t, publisher, "checkout", "-b", "feature-stale", "origin/feature-stale")
	if err := os.WriteFile(filepath.Join(publisher, "remote-advance.txt"), []byte("remote-advance\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runDaemonGit(t, publisher, "add", "remote-advance.txt")
	runDaemonGit(t, publisher, "commit", "-m", "remote-advance")
	shaB := strings.TrimSpace(string(mustCommandOutput(t, "git", "-C", publisher, "rev-parse", "HEAD")))
	runDaemonGit(t, publisher, "push", "origin", "feature-stale")
	if shaA == shaB {
		t.Fatal("setup failed: shaA and shaB should differ")
	}
	// Sanity check: the source repo still sees the stale SHA before the daemon runs.
	cached := strings.TrimSpace(string(mustCommandOutput(t, "git", "-C", repo, "rev-parse", "refs/remotes/origin/feature-stale")))
	if cached != shaA {
		t.Fatalf("setup failed: cached origin/feature-stale got %q, want stale SHA %q", cached, shaA)
	}

	writeRepoEnvironmentProfile(t, root, repo, "feature-stale")

	promptPath, schemaPath := writeDaemonPromptFiles(t)
	claudeBin := writeFakeClaude(t, "echo change > daemon-output.txt\necho '{\"status\":\"completed\",\"summary\":\"done\",\"files_modified\":[\"daemon-output.txt\"],\"acceptance_criteria\":[{\"id\":\"AC1\",\"status\":\"satisfied\",\"evidence\":[\"diff\"],\"notes\":\"done\"}],\"verification\":[],\"decisions\":[],\"risks\":[]}'\n")
	taskPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, taskPath, repo)

	if err := Run(context.Background(), Options{
		Root:               root,
		SystemPromptFile:   promptPath,
		JSONSchemaFile:     schemaPath,
		Supervisor:         "claude",
		ClaudeBin:          claudeBin,
		Once:               true,
		MaxConcurrentTasks: 1,
	}); err != nil {
		t.Fatal(err)
	}
	doneTask, err := task.Load(filepath.Join(root, "tasks", "done", "task.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	worktreePath := taskWorktreePath(repo, doneTask.Worktree.Path)
	got := strings.TrimSpace(string(mustCommandOutput(t, "git", "-C", worktreePath, "rev-parse", "HEAD")))
	if got != shaB {
		t.Fatalf("worktree HEAD got %q, want refreshed origin/feature-stale tip %q (stale was %q)", got, shaB, shaA)
	}
	// The local remote-tracking ref must have been refreshed by the daemon's
	// pre-resolve fetch, confirming refs/remotes/origin/feature-stale is no
	// longer stale.
	refreshed := strings.TrimSpace(string(mustCommandOutput(t, "git", "-C", repo, "rev-parse", "refs/remotes/origin/feature-stale")))
	if refreshed != shaB {
		t.Fatalf("refs/remotes/origin/feature-stale not refreshed; got %q want %q", refreshed, shaB)
	}
}

func writeRepoEnvironmentProfile(t *testing.T, root, repo, base string) {
	t.Helper()
	_, _, environmentPath, err := galleyhome.RepoProfilePaths(root, repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(environmentPath), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "id: env-test\n" +
		"cwd: " + strconv.Quote(repo) + "\n" +
		"commands: {}\n" +
		"constraints:\n" +
		"  network: approval_required\n" +
		"  secrets_policy: never_read_env_files\n" +
		"  destructive_commands: deny\n" +
		"pr:\n" +
		"  enabled: false\n" +
		"  base: " + base + "\n"
	if err := os.WriteFile(environmentPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
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
  if printf '%s' "$request" | grep -q '"status":"hard_stop"'; then
    printf '%s\n' '{"status":"hard_stop","summary":"executor reported hard_stop","acceptance_gaps":[],"reviewed_files":[],"acceptance_evidence":[],"findings":[{"severity":"high","category":"execution","file":"","summary":"executor reported hard_stop","blocks_acceptance":true}],"residual_risks":[],"discussion_items":[],"confidence":"high","next_work_order":""}'
  elif printf '%s' "$request" | grep -q '"parse_error":"'; then
    printf '%s\n' '{"status":"needs_revision","summary":"executor result was invalid","acceptance_gaps":["executor result JSON is invalid"],"reviewed_files":[],"acceptance_evidence":[],"findings":[{"severity":"medium","category":"verification","file":"","summary":"executor result JSON is invalid","blocks_acceptance":true}],"residual_risks":[],"discussion_items":[],"confidence":"high","next_work_order":"Return valid structured JSON and preserve any useful workspace changes."}'
  elif printf '%s' "$request" | grep -q '"status":"completed_with_risks"'; then
    printf '%s\n' '{"status":"needs_revision","summary":"executor reported risks","acceptance_gaps":["executor reported risks"],"reviewed_files":[],"acceptance_evidence":[],"findings":[{"severity":"medium","category":"verification","file":"","summary":"executor reported risks","blocks_acceptance":true}],"residual_risks":[],"discussion_items":[],"confidence":"high","next_work_order":"Resolve the reported risks and rerun verification."}'
  elif printf '%s' "$request" | grep -q '"diff_dirty":false'; then
    printf '%s\n' '{"status":"needs_revision","summary":"no repository diff was produced","acceptance_gaps":["no repository diff was produced"],"reviewed_files":[],"acceptance_evidence":[],"findings":[{"severity":"medium","category":"acceptance","file":"","summary":"no repository diff was produced","blocks_acceptance":true}],"residual_risks":[],"discussion_items":[],"confidence":"high","next_work_order":"Make the required repository change and return valid structured JSON."}'
  else
    printf '%s\n' '{"status":"accepted","summary":"fake claude supervisor accepted","acceptance_gaps":[],"reviewed_files":["daemon-output.txt"],"acceptance_evidence":[{"ac_id":"AC1","evidence":["diff"]}],"findings":[],"residual_risks":[],"discussion_items":[],"confidence":"high","next_work_order":""}'
  fi
  exit 0
fi
`+body)
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

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", path)
		}
		time.Sleep(5 * time.Millisecond)
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
  max_budget_usd: 0
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
