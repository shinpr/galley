package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shinpr/galley/internal/proc"
	"github.com/shinpr/galley/internal/task"
)

// finalizeRecoveryRepo builds the fixture shared by these tests: a source
// repo with a bare origin, plus a queued Galley task.
func finalizeRecoveryRepo(t *testing.T) (root, repo, remote, worktree, promptPath, schemaPath, taskPath string) {
	t.Helper()
	root = filepath.Join(t.TempDir(), ".agent-workflow")
	repo = initDaemonGitRepo(t)
	remote = filepath.Join(t.TempDir(), "remote.git")
	runDaemonGit(t, t.TempDir(), "init", "--bare", remote)
	runDaemonGit(t, repo, "remote", "add", "origin", remote)
	promptPath, schemaPath = writeDaemonPromptFiles(t)
	taskPath = filepath.Join(root, "tasks", "queued", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, taskPath, repo)
	worktree = filepath.Join(filepath.Dir(repo), "worktrees", "task")
	return root, repo, remote, worktree, promptPath, schemaPath, taskPath
}

// writeBlockingGitHook installs a real git hook that rejects the operation
// while marker exists, so finalization runs through hooks rather than around.
func writeBlockingGitHook(t *testing.T, repo, hook, marker, message string) {
	t.Helper()
	path := filepath.Join(repo, ".git", "hooks", hook)
	body := fmt.Sprintf("#!/bin/sh\nif [ -f %q ]; then\n  echo %q >&2\n  exit 1\nfi\nexit 0\n", marker, message)
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
}

func writeMarkerFile(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("blocked\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// finalizeRepairExecutor removes marker (the simulated failure cause) only
// after the pending finalization request arrives, and writes no file itself.
func finalizeRepairExecutor(t *testing.T, marker string) string {
	t.Helper()
	return writeFakeClaude(t, `case "$*" in
  *finalize-attempt-1*)
    rm -f `+marker+`
    echo '{"status":"completed","summary":"repaired finalization","files_modified":[],"acceptance_criteria":[{"id":"AC1","status":"satisfied","evidence":["diff"],"notes":"done"},{"id":"revision:finalize-attempt-1","status":"satisfied","evidence":["removed the blocking condition"],"notes":"finalization can rerun"}],"verification":[],"scope_expansions":[],"decisions":[],"risks":[]}'
    ;;
  *)
    echo change > daemon-output.txt
    echo '{"status":"completed","summary":"done","files_modified":["daemon-output.txt"],"acceptance_criteria":[{"id":"AC1","status":"satisfied","evidence":["diff"],"notes":"done"}],"verification":[],"scope_expansions":[],"decisions":[],"risks":[]}'
    ;;
esac
`)
}

func finalizeRecoveryOptions(root, promptPath, schemaPath, claudeBin, ghBin string) Options {
	return Options{
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
	}
}

func gitOutputForTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s failed: %v\n%s", args, dir, err, output)
	}
	return strings.TrimSpace(string(output))
}

func assertCommitsAheadOfBase(t *testing.T, worktree string, want int) {
	t.Helper()
	// The fixture repo has one initial commit, so the accepted work must add
	// exactly want commits on top of it.
	got := gitOutputForTest(t, worktree, "rev-list", "--count", "HEAD")
	if got != fmt.Sprintf("%d", want+1) {
		t.Fatalf("branch commit count got %s, want %d (initial + %d accepted)", got, want+1, want)
	}
}

func attemptPrompt(t *testing.T, root, attemptDir string) string {
	t.Helper()
	planData := mustReadSingleGlob(t, filepath.Join(root, "runs", "*", attemptDir, "command_plan.json"))
	var plan struct {
		Argv []string `json:"argv"`
	}
	if err := json.Unmarshal(planData, &plan); err != nil {
		t.Fatalf("decode %s command plan: %v", attemptDir, err)
	}
	if len(plan.Argv) == 0 {
		t.Fatalf("%s command plan argv is empty", attemptDir)
	}
	return plan.Argv[len(plan.Argv)-1]
}

func findRevisionRequest(t *testing.T, loaded task.Task, id string) task.RevisionRequest {
	t.Helper()
	for _, request := range loaded.RevisionRequests {
		if request.ID == id {
			return request
		}
	}
	t.Fatalf("revision request %q not found: %#v", id, loaded.RevisionRequests)
	return task.RevisionRequest{}
}

// AC1/AC2/AC4: a pre-commit hook failure becomes a pending finalization
// request in the next work order and recovers with exactly one commit.
func TestFinalizeCommitFailureRecoversThroughRevisionLoop(t *testing.T) {
	root, repo, remote, worktree, promptPath, schemaPath, taskPath := finalizeRecoveryRepo(t)
	marker := filepath.Join(t.TempDir(), "block-commit")
	writeMarkerFile(t, marker)
	writeBlockingGitHook(t, repo, "pre-commit", marker, "pre-commit hook rejected the accepted change")
	claudeBin := finalizeRepairExecutor(t, marker)
	ghBin := writeFakeCommand(t, "gh", `if [ "$1" = "pr" ] && [ "$2" = "create" ]; then
  echo https://github.com/example/galley/pull/501
else
  printf '%s\n' '{"user":{"login":"pr-author"}}'
fi
`)
	setLoopBudget(t, taskPath, 3)

	if err := runTestDaemon(context.Background(), finalizeRecoveryOptions(root, promptPath, schemaPath, claudeBin, ghBin)); err != nil {
		t.Fatal(err)
	}

	doneTask, err := task.Load(filepath.Join(root, "tasks", "done", "task.yaml"))
	if err != nil {
		t.Fatalf("task did not reach done/: %v", err)
	}
	if doneTask.Status != "pr_opened" {
		t.Fatalf("status got %q, want pr_opened", doneTask.Status)
	}
	if doneTask.PR.URL != "https://github.com/example/galley/pull/501" {
		t.Fatalf("pr got %#v", doneTask.PR)
	}
	if len(doneTask.Attempts) != 2 {
		t.Fatalf("attempts got %d, want 2 (the finalization failure must spend one more loop attempt): %#v", len(doneTask.Attempts), doneTask.Attempts)
	}
	if doneTask.Attempts[0].SupervisorVerdict != "accepted" || doneTask.Attempts[1].SupervisorVerdict != "accepted" {
		t.Fatalf("attempt verdicts got %#v, want two supervisor verdicts", doneTask.Attempts)
	}
	request := findRevisionRequest(t, doneTask, "finalize-attempt-1")
	if request.Source != "finalize" || request.Status != "addressed" {
		t.Fatalf("finalization revision request got %#v", request)
	}
	if !strings.Contains(request.Text, "pre-commit hook rejected the accepted change") {
		t.Fatalf("finalization revision request lost the captured command output: %q", request.Text)
	}
	if !strings.Contains(request.Text, filepath.Join(root, "runs")) {
		t.Fatalf("finalization revision request lost the run-artifact location: %q", request.Text)
	}

	summaries := 0
	for _, item := range doneTask.DiscussionItems {
		if item.Topic == "Supervisor summary" {
			summaries++
		}
	}
	if summaries != 1 {
		t.Fatalf("recovered acceptance duplicated discussion items: %#v", doneTask.DiscussionItems)
	}

	prompt := attemptPrompt(t, root, "attempt-2")
	for _, want := range []string{"finalize-attempt-1", "source=`finalize`", "pre-commit hook rejected the accepted change"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("attempt-2 work order missing %q:\n%s", want, prompt)
		}
	}

	effective, err := task.Load(filepath.Join(mustSingleGlobDir(t, filepath.Join(root, "runs", "*")), "attempt-2", "task.effective.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if effective.Scope.CWD != worktree {
		t.Fatalf("attempt-2 workspace got %q, want retained worktree %q", effective.Scope.CWD, worktree)
	}
	if effective.ReviewProgress == nil || len(effective.ReviewProgress.Acceptance) == 0 {
		t.Fatalf("attempt-2 lost accepted review progress: %#v", effective.ReviewProgress)
	}
	if _, err := os.Stat(worktree); err != nil {
		t.Fatalf("worktree was not preserved: %v", err)
	}
	assertCommitsAheadOfBase(t, worktree, 1)
	if got := gitOutputForTest(t, remote, "rev-parse", "refs/heads/agent/task"); got != gitOutputForTest(t, worktree, "rev-parse", "HEAD") {
		t.Fatalf("remote branch %s does not point at the accepted HEAD", got)
	}
	assertGlobCount(t, filepath.Join(root, "runs", "*", "gh_pr_create_result.json"), 1)
}

// AC1/AC2: a pre-push hook failure after the accepted commit exists recovers
// without a second commit, leaving the remote at the accepted HEAD.
func TestFinalizePushFailureRecoversThroughRevisionLoop(t *testing.T) {
	root, repo, remote, worktree, promptPath, schemaPath, taskPath := finalizeRecoveryRepo(t)
	marker := filepath.Join(t.TempDir(), "block-push")
	writeMarkerFile(t, marker)
	writeBlockingGitHook(t, repo, "pre-push", marker, "pre-push hook rejected the accepted branch")
	claudeBin := finalizeRepairExecutor(t, marker)
	ghBin := writeFakeCommand(t, "gh", `if [ "$1" = "pr" ] && [ "$2" = "create" ]; then
  echo https://github.com/example/galley/pull/502
else
  printf '%s\n' '{"user":{"login":"pr-author"}}'
fi
`)
	setLoopBudget(t, taskPath, 3)

	if err := runTestDaemon(context.Background(), finalizeRecoveryOptions(root, promptPath, schemaPath, claudeBin, ghBin)); err != nil {
		t.Fatal(err)
	}

	doneTask, err := task.Load(filepath.Join(root, "tasks", "done", "task.yaml"))
	if err != nil {
		t.Fatalf("task did not reach done/: %v", err)
	}
	if doneTask.Status != "pr_opened" || doneTask.PR.URL != "https://github.com/example/galley/pull/502" {
		t.Fatalf("recovered task got status=%q pr=%#v", doneTask.Status, doneTask.PR)
	}
	request := findRevisionRequest(t, doneTask, "finalize-attempt-1")
	if !strings.Contains(request.Text, "pre-push hook rejected the accepted branch") {
		t.Fatalf("finalization revision request lost the push failure output: %q", request.Text)
	}
	assertCommitsAheadOfBase(t, worktree, 1)
	if got := gitOutputForTest(t, remote, "rev-parse", "refs/heads/agent/task"); got != gitOutputForTest(t, worktree, "rev-parse", "HEAD") {
		t.Fatalf("remote branch %s does not point at the accepted HEAD", got)
	}
	assertGlobCount(t, filepath.Join(root, "runs", "*", "gh_pr_create_result.json"), 1)
}

// AC1/AC2: a failed PR creation with no recoverable PR recovers with exactly
// one recorded PR URL and no extra commit.
func TestFinalizePRCreateFailureRecoversThroughRevisionLoop(t *testing.T) {
	root, _, _, worktree, promptPath, schemaPath, taskPath := finalizeRecoveryRepo(t)
	marker := filepath.Join(t.TempDir(), "block-pr-create")
	writeMarkerFile(t, marker)
	claudeBin := finalizeRepairExecutor(t, marker)
	ghBin := writeFakeCommand(t, "gh", `if [ "$1" = "pr" ] && [ "$2" = "create" ]; then
  if [ -f `+marker+` ]; then
    echo "gh pr create rejected the request: HTTP 422 base branch is protected" >&2
    exit 1
  fi
  echo https://github.com/example/galley/pull/503
elif [ "$1" = "pr" ] && [ "$2" = "view" ]; then
  echo "no pull requests found for branch" >&2
  exit 1
else
  printf '%s\n' '{"user":{"login":"pr-author"}}'
fi
`)
	setLoopBudget(t, taskPath, 3)

	if err := runTestDaemon(context.Background(), finalizeRecoveryOptions(root, promptPath, schemaPath, claudeBin, ghBin)); err != nil {
		t.Fatal(err)
	}

	doneTask, err := task.Load(filepath.Join(root, "tasks", "done", "task.yaml"))
	if err != nil {
		t.Fatalf("task did not reach done/: %v", err)
	}
	if doneTask.Status != "pr_opened" || doneTask.PR.URL != "https://github.com/example/galley/pull/503" || doneTask.PR.Status != "open" {
		t.Fatalf("recovered task got status=%q pr=%#v", doneTask.Status, doneTask.PR)
	}
	request := findRevisionRequest(t, doneTask, "finalize-attempt-1")
	if !strings.Contains(request.Text, "HTTP 422 base branch is protected") {
		t.Fatalf("finalization revision request lost the gh pr create output: %q", request.Text)
	}
	assertCommitsAheadOfBase(t, worktree, 1)
}

// AC3: after attempt exhaustion, a plain requeue finalizes the retained
// worktree whose accepted commit exists, without a false empty diff.
func TestFinalizeFailureSurvivesRequeueAndFinalizesRetainedCommit(t *testing.T) {
	root, repo, remote, worktree, promptPath, schemaPath, taskPath := finalizeRecoveryRepo(t)
	marker := filepath.Join(t.TempDir(), "block-push")
	writeMarkerFile(t, marker)
	writeBlockingGitHook(t, repo, "pre-push", marker, "pre-push hook rejected the accepted branch")
	firstClaude := writeFakeClaude(t, `echo change > daemon-output.txt
echo '{"status":"completed","summary":"done","files_modified":["daemon-output.txt"],"acceptance_criteria":[{"id":"AC1","status":"satisfied","evidence":["diff"],"notes":"done"}],"verification":[],"scope_expansions":[],"decisions":[],"risks":[]}'
`)
	ghBin := writeFakeCommand(t, "gh", `if [ "$1" = "pr" ] && [ "$2" = "create" ]; then
  echo https://github.com/example/galley/pull/504
else
  printf '%s\n' '{"user":{"login":"pr-author"}}'
fi
`)
	setLoopBudget(t, taskPath, 1)

	if err := runTestDaemon(context.Background(), finalizeRecoveryOptions(root, promptPath, schemaPath, firstClaude, ghBin)); err != nil {
		t.Fatal(err)
	}

	failedPath := filepath.Join(root, "tasks", "failed", "task.yaml")
	failedTask, err := task.Load(failedPath)
	if err != nil {
		t.Fatalf("first run did not publish a failed task: %v", err)
	}
	if failedTask.Status != "failed" {
		t.Fatalf("first run status got %q, want failed", failedTask.Status)
	}
	pending := findRevisionRequest(t, failedTask, "finalize-attempt-1")
	if pending.Status != "pending" || !strings.Contains(pending.Text, "pre-push hook rejected the accepted branch") {
		t.Fatalf("pending finalization revision was not preserved: %#v", pending)
	}
	acceptedHead := gitOutputForTest(t, worktree, "rev-parse", "HEAD")
	assertCommitsAheadOfBase(t, worktree, 1)
	firstBase := runBaseSHA(t, root)

	// A plain requeue: no new revision request and no human-authored guidance.
	if _, err := task.Requeue(failedPath, task.RequeueOptions{Root: root}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}

	// The second run's executor changes nothing: recovery comes from the
	// retained worktree, retained review base, and the pending request.
	secondClaude := writeFakeClaude(t, `echo '{"status":"completed","summary":"finalization blocker cleared","files_modified":[],"acceptance_criteria":[{"id":"AC1","status":"satisfied","evidence":["retained commit"],"notes":"done"},{"id":"revision:finalize-attempt-1","status":"satisfied","evidence":["blocking condition resolved"],"notes":"finalization can rerun"}],"verification":[],"scope_expansions":[],"decisions":[],"risks":[]}'
`)
	if err := runTestDaemon(context.Background(), finalizeRecoveryOptions(root, promptPath, schemaPath, secondClaude, ghBin)); err != nil {
		t.Fatal(err)
	}

	doneTask, err := task.Load(filepath.Join(root, "tasks", "done", "task.yaml"))
	if err != nil {
		t.Fatalf("requeued task did not reach done/: %v", err)
	}
	if doneTask.Status != "pr_opened" || doneTask.PR.URL != "https://github.com/example/galley/pull/504" {
		t.Fatalf("requeued task got status=%q pr=%#v", doneTask.Status, doneTask.PR)
	}
	if got := gitOutputForTest(t, worktree, "rev-parse", "HEAD"); got != acceptedHead {
		t.Fatalf("accepted commit changed across requeue: got %s want %s", got, acceptedHead)
	}
	assertCommitsAheadOfBase(t, worktree, 1)
	if got := gitOutputForTest(t, remote, "rev-parse", "refs/heads/agent/task"); got != acceptedHead {
		t.Fatalf("remote branch %s does not point at the accepted HEAD %s", got, acceptedHead)
	}
	if got := gitOutputForTest(t, worktree, "show", "HEAD:daemon-output.txt"); got != "change" {
		t.Fatalf("accepted implementation change was lost: %q", got)
	}
	if got := secondRunBaseSHA(t, root); got != firstBase {
		t.Fatalf("second run review base got %s, want the retained base %s", got, firstBase)
	}
	assertGlobCount(t, filepath.Join(root, "runs", "*", "gh_pr_create_result.json"), 1)
}

// The request text carries the actionable tail of the captured command
// output within the documented budget instead of the whole log.
func TestFinalizeRevisionRequestBoundsCapturedOutput(t *testing.T) {
	stderr := strings.Repeat("x", finalizeOutputBudget*2) + "pre-push hook rejected the accepted branch"
	err := fmt.Errorf("git push failed: %w", &proc.CommandError{
		Kind:   proc.CommandErrorExitNonZero,
		Result: proc.RunResult{ExitCode: 1, Stderr: stderr},
		Err:    errors.New("exit nonzero"),
	})
	request := finalizeRevisionRequest("finalize-attempt-2", "/runs/task-1", err)
	if request.ID != "finalize-attempt-2" || request.Source != finalizeRevisionSource || request.Status != "pending" {
		t.Fatalf("request got %#v", request)
	}
	if !strings.Contains(request.Text, "pre-push hook rejected the accepted branch") {
		t.Fatalf("request lost the actionable output tail: %q", request.Text)
	}
	if !strings.Contains(request.Text, "/runs/task-1") {
		t.Fatalf("request lost the run-artifact location: %q", request.Text)
	}
	if !strings.Contains(request.Text, "[truncated]") || len(request.Text) > finalizeOutputBudget*2 {
		t.Fatalf("request output was not bounded: len=%d", len(request.Text))
	}
}

func mustSingleGlobDir(t *testing.T, pattern string) string {
	t.Helper()
	matches, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("%s matched %d dirs, want 1: %#v", pattern, len(matches), matches)
	}
	return matches[0]
}

func runBaseSHA(t *testing.T, root string) string {
	t.Helper()
	return decodeBaseSHA(t, mustReadSingleGlob(t, filepath.Join(root, "runs", "*", "workspace.json")))
}

func secondRunBaseSHA(t *testing.T, root string) string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(root, "runs", "*", "workspace.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 2 {
		t.Fatalf("want two run workspace evidence files, got %#v", matches)
	}
	data, err := os.ReadFile(matches[1])
	if err != nil {
		t.Fatal(err)
	}
	return decodeBaseSHA(t, data)
}

func decodeBaseSHA(t *testing.T, data []byte) string {
	t.Helper()
	var prepared struct {
		BaseSHA string `json:"base_sha"`
	}
	if err := json.Unmarshal(data, &prepared); err != nil {
		t.Fatal(err)
	}
	return prepared.BaseSHA
}

// writeHookMessage sets the message the blocking hook reports, so each run's
// failure output is distinguishable in the persisted revision request.
func writeHookMessage(t *testing.T, path, message string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(message+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// writeMessageEchoingGitHook installs a real hook that rejects the operation
// with the current contents of marker while that file exists.
func writeMessageEchoingGitHook(t *testing.T, repo, hook, marker string) {
	t.Helper()
	path := filepath.Join(repo, ".git", "hooks", hook)
	body := fmt.Sprintf("#!/bin/sh\nif [ -f %q ]; then\n  cat %q >&2\n  exit 1\nfi\nexit 0\n", marker, marker)
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
}

// finalizeRevisionProjectingClaude is the shared fake claude whose supervisor
// also passes revision:finalize-attempt-1 once that request reaches it.
func finalizeRevisionProjectingClaude(t *testing.T, executorBody string) string {
	t.Helper()
	return writeFakeCommand(t, "claude", `supervisor=0
for arg in "$@"; do
  if [ "$arg" = "--no-session-persistence" ]; then
    supervisor=1
  fi
done
if [ "$supervisor" = "1" ]; then
  request="$(cat)"
  if printf '%s' "$request" | grep -q 'finalize-attempt-1'; then
    printf '%s\n' '{"status":"accepted","summary":"accepted with the finalization revision","acceptance_passes":["AC1","revision:finalize-attempt-1"],"quality_passes":[],"findings":[],"discussion_items":[]}'
  else
    printf '%s\n' '{"status":"accepted","summary":"accepted","acceptance_passes":["AC1"],"quality_passes":[],"findings":[],"discussion_items":[]}'
  fi
  exit 0
fi
`+executorBody+`
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"session_id":"fake-claude-session"}'
`)
}

// AC1/AC3: a repeated finalization failure must leave the latest failure
// pending, and a further plain requeue must publish exactly one PR.
func TestFinalizeFailureRepeatedAfterRequeueKeepsLatestPendingRevision(t *testing.T) {
	root, repo, remote, worktree, promptPath, schemaPath, taskPath := finalizeRecoveryRepo(t)
	marker := filepath.Join(t.TempDir(), "block-push")
	writeHookMessage(t, marker, "pre-push hook rejected the accepted branch (run 1)")
	writeMessageEchoingGitHook(t, repo, "pre-push", marker)
	ghBin := writeFakeCommand(t, "gh", `if [ "$1" = "pr" ] && [ "$2" = "create" ]; then
  echo https://github.com/example/galley/pull/505
else
  printf '%s\n' '{"user":{"login":"pr-author"}}'
fi
`)
	setLoopBudget(t, taskPath, 1)

	firstClaude := finalizeRevisionProjectingClaude(t, `echo change > daemon-output.txt
echo '{"status":"completed","summary":"done","files_modified":["daemon-output.txt"],"acceptance_criteria":[{"id":"AC1","status":"satisfied","evidence":["diff"],"notes":"done"}],"verification":[],"scope_expansions":[],"decisions":[],"risks":[]}'
`)
	if err := runTestDaemon(context.Background(), finalizeRecoveryOptions(root, promptPath, schemaPath, firstClaude, ghBin)); err != nil {
		t.Fatal(err)
	}

	failedPath := filepath.Join(root, "tasks", "failed", "task.yaml")
	failedTask, err := task.Load(failedPath)
	if err != nil {
		t.Fatalf("first run did not publish a failed task: %v", err)
	}
	first := findRevisionRequest(t, failedTask, "finalize-attempt-1")
	if first.Status != "pending" || !strings.Contains(first.Text, "(run 1)") {
		t.Fatalf("first run pending finalization request got %#v", first)
	}
	acceptedHead := gitOutputForTest(t, worktree, "rev-parse", "HEAD")
	firstBase := runBaseSHA(t, root)

	// A plain requeue whose finalization fails again on its first attempt.
	if _, err := task.Requeue(failedPath, task.RequeueOptions{Root: root}); err != nil {
		t.Fatal(err)
	}
	writeHookMessage(t, marker, "pre-push hook rejected the accepted branch (run 2)")
	retainedClaude := finalizeRevisionProjectingClaude(t, `echo '{"status":"completed","summary":"finalization blocker addressed","files_modified":[],"acceptance_criteria":[{"id":"AC1","status":"satisfied","evidence":["retained commit"],"notes":"done"},{"id":"revision:finalize-attempt-1","status":"satisfied","evidence":["blocking condition resolved"],"notes":"finalization can rerun"}],"verification":[],"scope_expansions":[],"decisions":[],"risks":[]}'
`)
	if err := runTestDaemon(context.Background(), finalizeRecoveryOptions(root, promptPath, schemaPath, retainedClaude, ghBin)); err != nil {
		t.Fatal(err)
	}

	secondFailed, err := task.Load(failedPath)
	if err != nil {
		t.Fatalf("second run did not publish a failed task: %v", err)
	}
	pending := findRevisionRequest(t, secondFailed, "finalize-attempt-1")
	if pending.Status != "pending" {
		t.Fatalf("second finalization failure left request status %q, want pending: %#v", pending.Status, pending)
	}
	if !strings.Contains(pending.Text, "git push failed") {
		t.Fatalf("pending request lost the failed operation: %q", pending.Text)
	}
	if !strings.Contains(pending.Text, "(run 2)") || strings.Contains(pending.Text, "(run 1)") {
		t.Fatalf("pending request does not describe the latest failure: %q", pending.Text)
	}
	if !strings.Contains(pending.Text, nthRunDir(t, root, 2)) {
		t.Fatalf("pending request lost the second run's artifact location: %q", pending.Text)
	}
	if pending.Evidence != "" {
		t.Fatalf("pending request kept stale addressed evidence: %q", pending.Evidence)
	}
	if secondFailed.ReviewProgress == nil || len(secondFailed.ReviewProgress.Acceptance) == 0 {
		t.Fatalf("second run lost prior review passes: %#v", secondFailed.ReviewProgress)
	}
	if got := secondRunBaseSHA(t, root); got != firstBase {
		t.Fatalf("second run review base got %s, want the retained base %s", got, firstBase)
	}
	if got := gitOutputForTest(t, worktree, "rev-parse", "HEAD"); got != acceptedHead {
		t.Fatalf("accepted commit changed across the second run: got %s want %s", got, acceptedHead)
	}
	assertCommitsAheadOfBase(t, worktree, 1)

	// The final plain requeue finalizes the retained accepted commit.
	if _, err := task.Requeue(failedPath, task.RequeueOptions{Root: root}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	if err := runTestDaemon(context.Background(), finalizeRecoveryOptions(root, promptPath, schemaPath, retainedClaude, ghBin)); err != nil {
		t.Fatal(err)
	}

	doneTask, err := task.Load(filepath.Join(root, "tasks", "done", "task.yaml"))
	if err != nil {
		t.Fatalf("requeued task did not reach done/: %v", err)
	}
	if doneTask.Status != "pr_opened" || doneTask.PR.URL != "https://github.com/example/galley/pull/505" {
		t.Fatalf("requeued task got status=%q pr=%#v", doneTask.Status, doneTask.PR)
	}
	if got := findRevisionRequest(t, doneTask, "finalize-attempt-1"); got.Status != "addressed" {
		t.Fatalf("successful finalization did not address the request: %#v", got)
	}
	if got := gitOutputForTest(t, worktree, "rev-parse", "HEAD"); got != acceptedHead {
		t.Fatalf("accepted commit changed across requeue: got %s want %s", got, acceptedHead)
	}
	assertCommitsAheadOfBase(t, worktree, 1)
	if got := gitOutputForTest(t, remote, "rev-parse", "refs/heads/agent/task"); got != acceptedHead {
		t.Fatalf("remote branch %s does not point at the accepted HEAD %s", got, acceptedHead)
	}
	if got := gitOutputForTest(t, worktree, "show", "HEAD:daemon-output.txt"); got != "change" {
		t.Fatalf("accepted implementation change was lost: %q", got)
	}
	assertGlobCount(t, filepath.Join(root, "runs", "*", "gh_pr_create_result.json"), 1)
}

// seedRevisionRequest adds a request from another source to a task file, so
// Galley's own finalization identity has to avoid the ID it already holds.
func seedRevisionRequest(t *testing.T, path string, request task.RevisionRequest) {
	t.Helper()
	loaded, err := task.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	loaded.RevisionRequests = append(loaded.RevisionRequests, request)
	if err := task.Save(path, loaded); err != nil {
		t.Fatal(err)
	}
}

// AC1: a finalization failure must not overwrite a request from another source
// that already holds the ID Galley would otherwise use for this attempt.
func TestFinalizeFailurePreservesForeignRequestHoldingItsID(t *testing.T) {
	root, repo, remote, worktree, promptPath, schemaPath, taskPath := finalizeRecoveryRepo(t)
	foreign := task.RevisionRequest{
		ID:        "finalize-attempt-1",
		Source:    "pr-comment",
		CommentID: "IC_human_1",
		Text:      "Reviewer asked for the retry budget to stay documented in the goal.",
		Status:    "pending",
		Evidence:  "requested in the PR review thread",
	}
	seedRevisionRequest(t, taskPath, foreign)
	marker := filepath.Join(t.TempDir(), "block-push")
	writeMarkerFile(t, marker)
	writeBlockingGitHook(t, repo, "pre-push", marker, "pre-push hook rejected the accepted branch")
	claudeBin := writeFakeClaude(t, `case "$*" in
  *finalize-attempt-1-2*)
    rm -f `+marker+`
    echo '{"status":"completed","summary":"repaired finalization","files_modified":[],"acceptance_criteria":[{"id":"AC1","status":"satisfied","evidence":["diff"],"notes":"done"},{"id":"revision:finalize-attempt-1","status":"satisfied","evidence":["budget documented"],"notes":"reviewer request kept"},{"id":"revision:finalize-attempt-1-2","status":"satisfied","evidence":["removed the blocking condition"],"notes":"finalization can rerun"}],"verification":[],"scope_expansions":[],"decisions":[],"risks":[]}'
    ;;
  *)
    echo change > daemon-output.txt
    echo '{"status":"completed","summary":"done","files_modified":["daemon-output.txt"],"acceptance_criteria":[{"id":"AC1","status":"satisfied","evidence":["diff"],"notes":"done"},{"id":"revision:finalize-attempt-1","status":"satisfied","evidence":["budget documented"],"notes":"reviewer request kept"}],"verification":[],"scope_expansions":[],"decisions":[],"risks":[]}'
    ;;
esac
`)
	ghBin := writeFakeCommand(t, "gh", `if [ "$1" = "pr" ] && [ "$2" = "create" ]; then
  echo https://github.com/example/galley/pull/506
else
  printf '%s\n' '{"user":{"login":"pr-author"}}'
fi
`)
	setLoopBudget(t, taskPath, 3)

	if err := runTestDaemon(context.Background(), finalizeRecoveryOptions(root, promptPath, schemaPath, claudeBin, ghBin)); err != nil {
		t.Fatal(err)
	}

	// Both instructions, identities, and provenance must reach the next
	// attempt's work order side by side.
	prompt := attemptPrompt(t, root, "attempt-2")
	for _, want := range []string{
		"`finalize-attempt-1` source=`pr-comment` comment=`IC_human_1`: " + foreign.Text,
		"`finalize-attempt-1-2` source=`finalize`",
		"pre-push hook rejected the accepted branch",
		filepath.Join(root, "runs"),
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("attempt-2 work order missing %q:\n%s", want, prompt)
		}
	}

	doneTask, err := task.Load(filepath.Join(root, "tasks", "done", "task.yaml"))
	if err != nil {
		t.Fatalf("task did not reach done/: %v", err)
	}
	if doneTask.Status != "pr_opened" || doneTask.PR.URL != "https://github.com/example/galley/pull/506" {
		t.Fatalf("recovered task got status=%q pr=%#v", doneTask.Status, doneTask.PR)
	}
	if got := findRevisionRequest(t, doneTask, "finalize-attempt-1"); got != foreign {
		t.Fatalf("the pr-comment request was not preserved: got %#v want %#v", got, foreign)
	}
	own := findRevisionRequest(t, doneTask, "finalize-attempt-1-2")
	if own.Source != finalizeRevisionSource || own.Status != "addressed" {
		t.Fatalf("Galley's finalization request got %#v", own)
	}
	if !strings.Contains(own.Text, "git push failed") || !strings.Contains(own.Text, "pre-push hook rejected the accepted branch") {
		t.Fatalf("Galley's finalization request lost the captured failure: %q", own.Text)
	}
	if !strings.Contains(own.Text, filepath.Join(root, "runs")) {
		t.Fatalf("Galley's finalization request lost the run-artifact location: %q", own.Text)
	}
	assertCommitsAheadOfBase(t, worktree, 1)
	if got := gitOutputForTest(t, remote, "rev-parse", "refs/heads/agent/task"); got != gitOutputForTest(t, worktree, "rev-parse", "HEAD") {
		t.Fatalf("remote branch %s does not point at the accepted HEAD", got)
	}
	assertGlobCount(t, filepath.Join(root, "runs", "*", "gh_pr_create_result.json"), 1)
}

// A repeated failure on the same attempt must reopen Galley's own request under
// its derived identity instead of accumulating one request per requeue.
func TestFinalizeRevisionIdentityIsStableBesideAForeignRequest(t *testing.T) {
	loaded := task.Task{RevisionRequests: []task.RevisionRequest{{
		ID: "finalize-attempt-1", Source: "pr-comment", Text: "human instruction", Status: "pending",
	}}}
	for run := 1; run <= 3; run++ {
		id := finalizeRevisionID(loaded.RevisionRequests, 1)
		if id != "finalize-attempt-1-2" {
			t.Fatalf("run %d derived id got %q", run, id)
		}
		request := finalizeRevisionRequest(id, "/runs/run-"+fmt.Sprint(run), fmt.Errorf("git push failed (run %d)", run))
		upsertFinalizeRevision(&loaded, request)
	}
	if len(loaded.RevisionRequests) != 2 {
		t.Fatalf("repeated failures did not reuse one identity: %#v", loaded.RevisionRequests)
	}
	if loaded.RevisionRequests[0].Source != "pr-comment" || loaded.RevisionRequests[0].Text != "human instruction" {
		t.Fatalf("the pr-comment request was overwritten: %#v", loaded.RevisionRequests[0])
	}
	if got := loaded.RevisionRequests[1]; !strings.Contains(got.Text, "(run 3)") || got.Status != "pending" {
		t.Fatalf("latest failure was not the pending request: %#v", got)
	}
}

// nthRunDir returns the nth (1-based) run directory in chronological order.
func nthRunDir(t *testing.T, root string, n int) string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(root, "runs", "*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) < n {
		t.Fatalf("want at least %d run dirs, got %#v", n, matches)
	}
	return matches[n-1]
}
