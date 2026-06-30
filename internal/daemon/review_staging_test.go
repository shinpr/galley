package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shinpr/galley/internal/task"
)

// TestRunOnceStagesNewUntrackedFileBeforeSupervisorReview pins AC1+AC2: when
// the fake executor creates a new file but does not run `git add`, Galley's
// review-time staging step must make that file visible in the attempt
// diff.patch so the supervisor evaluates the actual change instead of an
// empty submitted diff.
func TestRunOnceStagesNewUntrackedFileBeforeSupervisorReview(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	promptPath, schemaPath := writeDaemonPromptFiles(t)
	// Fake executor writes an untracked file but does NOT call `git add`.
	claudeBin := writeFakeClaude(t, "echo executor-output > new-untracked-file.txt\necho '{\"status\":\"completed\",\"summary\":\"done\",\"files_modified\":[\"new-untracked-file.txt\"],\"acceptance_criteria\":[{\"id\":\"AC1\",\"status\":\"satisfied\",\"evidence\":[\"diff\"],\"notes\":\"done\"}],\"verification\":[],\"scope_expansions\":[],\"decisions\":[],\"risks\":[]}'\n")
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

	// attempt diff.patch must contain a new-file header and the executor
	// content for the previously untracked path.
	diff := string(mustReadSingleGlob(t, filepath.Join(root, "runs", "*", "attempt-1", "diff.patch")))
	if !strings.Contains(diff, "new-untracked-file.txt") {
		t.Fatalf("attempt diff.patch missing untracked file path:\n%s", diff)
	}
	if !strings.Contains(diff, "new file mode") {
		t.Fatalf("attempt diff.patch missing new file header:\n%s", diff)
	}
	if !strings.Contains(diff, "executor-output") {
		t.Fatalf("attempt diff.patch missing executor content:\n%s", diff)
	}

	// AC6 evidence: review-time staging command result must be persisted.
	assertGlobCount(t, filepath.Join(root, "runs", "*", "attempt-1", "git_add_review_result.json"), 1)
}

// TestRunOnceAcceptedFinalizationCommitsStagedNewFile pins AC3: when the
// executor leaves a new file untracked and the supervisor accepts, the
// existing Galley finalization path still produces a final commit whose
// committed tree contains the new file.
func TestRunOnceAcceptedFinalizationCommitsStagedNewFile(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	runDaemonGit(t, t.TempDir(), "init", "--bare", remote)
	runDaemonGit(t, repo, "remote", "add", "origin", remote)
	promptPath, schemaPath := writeDaemonPromptFiles(t)
	// Fake executor writes the file but does NOT call `git add`; the fake
	// claude supervisor accepts any diff_dirty=true attempt that does not
	// report parse/hard_stop/empty diff.
	claudeBin := writeFakeClaude(t, "echo daemon-output-content > daemon-output.txt\necho '{\"status\":\"completed\",\"summary\":\"done\",\"files_modified\":[\"daemon-output.txt\"],\"acceptance_criteria\":[{\"id\":\"AC1\",\"status\":\"satisfied\",\"evidence\":[\"diff\"],\"notes\":\"done\"}],\"verification\":[],\"scope_expansions\":[],\"decisions\":[],\"risks\":[]}'\n")
	ghBin := writeFakeCommand(t, "gh", "echo https://github.com/example/galley/pull/555\n")
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
		OpenPR:             true,
		PRBase:             "main",
		GHBin:              ghBin,
	}); err != nil {
		t.Fatal(err)
	}

	doneTask, err := task.Load(filepath.Join(root, "tasks", "done", "task.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if doneTask.Status != "pr_opened" {
		t.Fatalf("status got %q want pr_opened", doneTask.Status)
	}
	worktreePath := taskWorktreePath(repo, doneTask.Worktree.Path)
	// The committed HEAD tree must contain the previously untracked file.
	committed := strings.TrimSpace(string(mustCommandOutput(t, "git", "-C", worktreePath, "show", "HEAD:daemon-output.txt")))
	if committed != "daemon-output-content" {
		t.Fatalf("committed file content got %q want %q", committed, "daemon-output-content")
	}
}

// TestRunOnceAcceptedFinalizationCommitsStagedOnlyDeletion pins AC1+AC3 for a
// staged-only deletion. The fake executor stages the deletion of a tracked
// file with `git rm` and submits no other change. Before the fix, review
// staging ran `git add -A -- <deleted path>` and failed with "pathspec did not
// match any files", so the attempt never reached the supervisor. After the
// fix, review staging skips the already-staged deletion (it stays visible in
// the captured attempt diff), the supervisor accepts the dirty diff, and
// finalization commits the deletion without re-adding it.
func TestRunOnceAcceptedFinalizationCommitsStagedOnlyDeletion(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	runDaemonGit(t, t.TempDir(), "init", "--bare", remote)
	runDaemonGit(t, repo, "remote", "add", "origin", remote)
	promptPath, schemaPath := writeDaemonPromptFiles(t)
	// Executor stages a deletion of the tracked README.md and nothing else.
	// `git rm` removes it from both the worktree and the index, producing a
	// "D " staged-only deletion in git status.
	claudeBin := writeFakeClaude(t, "git rm README.md\necho '{\"status\":\"completed\",\"summary\":\"done\",\"files_modified\":[\"README.md\"],\"acceptance_criteria\":[{\"id\":\"AC1\",\"status\":\"satisfied\",\"evidence\":[\"diff\"],\"notes\":\"done\"}],\"verification\":[],\"decisions\":[],\"risks\":[]}'\n")
	ghBin := writeFakeCommand(t, "gh", "echo https://github.com/example/galley/pull/999\n")
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
		OpenPR:             true,
		PRBase:             "main",
		GHBin:              ghBin,
	}); err != nil {
		t.Fatal(err)
	}

	// AC1: the staged deletion must remain visible in the captured attempt
	// diff even though review staging did not re-add it.
	diff := string(mustReadSingleGlob(t, filepath.Join(root, "runs", "*", "attempt-1", "diff.patch")))
	if !strings.Contains(diff, "README.md") || !strings.Contains(diff, "deleted file mode") {
		t.Fatalf("attempt diff.patch missing staged deletion of README.md:\n%s", diff)
	}

	doneTask, err := task.Load(filepath.Join(root, "tasks", "done", "task.yaml"))
	if err != nil {
		t.Fatalf("task did not reach done/: %v", err)
	}
	if doneTask.Status != "pr_opened" {
		t.Fatalf("status got %q want pr_opened", doneTask.Status)
	}
	worktreePath := taskWorktreePath(repo, doneTask.Worktree.Path)
	// AC3: the final commit must record the deletion and HEAD must no longer
	// contain README.md.
	nameStatus := string(mustCommandOutput(t, "git", "-C", worktreePath, "show", "--name-status", "--format=", "HEAD"))
	if !strings.Contains(nameStatus, "D\tREADME.md") {
		t.Fatalf("final commit does not record README.md deletion:\n%s", nameStatus)
	}
	tree := string(mustCommandOutput(t, "git", "-C", worktreePath, "ls-tree", "-r", "--name-only", "HEAD"))
	if strings.Contains(tree, "README.md") {
		t.Fatalf("README.md still present in HEAD tree after staged deletion was finalized:\n%s", tree)
	}
}

// TestRunOnceAcceptedFinalizationExcludesNonCommittedInputFile pins AC4:
// review-time staging must not cause a commit:false input file to be
// committed. After the executor (without running `git add`) introduces a
// real change, the final commit must include only the real change, not the
// non-committed input file Galley placed in the worktree.
func TestRunOnceAcceptedFinalizationExcludesNonCommittedInputFile(t *testing.T) {
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
	claudeBin := writeFakeClaude(t, "echo change > daemon-output.txt\necho '{\"status\":\"completed\",\"summary\":\"done\",\"files_modified\":[\"daemon-output.txt\"],\"acceptance_criteria\":[{\"id\":\"AC1\",\"status\":\"satisfied\",\"evidence\":[\"diff\"],\"notes\":\"done\"}],\"verification\":[],\"scope_expansions\":[],\"decisions\":[],\"risks\":[]}'\n")
	ghBin := writeFakeCommand(t, "gh", "echo https://github.com/example/galley/pull/777\n")
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

	if err := Run(context.Background(), Options{
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
	}); err != nil {
		t.Fatal(err)
	}

	doneTask, err := task.Load(filepath.Join(root, "tasks", "done", "task.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	worktreePath := taskWorktreePath(repo, doneTask.Worktree.Path)
	// Non-committed input file destination must not be present in the HEAD
	// tree even though review-time staging happened before finalization.
	committedFiles, err := exec.Command("git", "-C", worktreePath, "show", "--name-only", "--format=", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(committedFiles), "docs/plan.md") {
		t.Fatalf("commit:false input file leaked into HEAD commit:\n%s", committedFiles)
	}
	if !strings.Contains(string(committedFiles), "daemon-output.txt") {
		t.Fatalf("expected executor diff to be committed:\n%s", committedFiles)
	}
}

// TestRunOnceAcceptedFinalizationDetectsForbiddenPathAfterStaging pins AC5:
// review-time staging does not bypass the forbidden-path check in accepted
// finalization. When the executor creates a file under
// task.scope.forbidden_paths and does not stage it, finalization must still
// fail with the existing forbidden-path error and the task must not reach
// pr_opened.
func TestRunOnceAcceptedFinalizationDetectsForbiddenPathAfterStaging(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	runDaemonGit(t, t.TempDir(), "init", "--bare", remote)
	runDaemonGit(t, repo, "remote", "add", "origin", remote)
	promptPath, schemaPath := writeDaemonPromptFiles(t)
	// Executor writes a file under a forbidden directory (no `git add`).
	claudeBin := writeFakeClaude(t, "mkdir -p secret\necho secret > secret/leak.txt\necho '{\"status\":\"completed\",\"summary\":\"done\",\"files_modified\":[\"secret/leak.txt\"],\"acceptance_criteria\":[{\"id\":\"AC1\",\"status\":\"satisfied\",\"evidence\":[\"diff\"],\"notes\":\"done\"}],\"verification\":[],\"scope_expansions\":[],\"decisions\":[],\"risks\":[]}'\n")
	ghBin := writeFakeCommand(t, "gh", "echo https://github.com/example/galley/pull/888\n")
	taskPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, taskPath, repo)
	loaded, err := task.Load(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Scope.ForbiddenPaths = []string{"secret"}
	if err := task.Save(taskPath, loaded); err != nil {
		t.Fatal(err)
	}

	// AC5: finalize must surface the forbidden-path failure. Run reports the
	// failure via its return value (matching the daemon's other terminal
	// failure paths such as TestRunOnceFailsWhenPRBaseRefMissing) and also
	// moves the task to tasks/failed for inspection.
	runErr := Run(context.Background(), Options{
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
	if runErr == nil {
		t.Fatal("expected forbidden-path failure to be surfaced by Run")
	}
	if !strings.Contains(runErr.Error(), "task.scope.forbidden_paths") {
		t.Fatalf("Run error does not mention forbidden_paths: %v", runErr)
	}

	failedTask, err := task.Load(filepath.Join(root, "tasks", "failed", "task.yaml"))
	if err != nil {
		t.Fatalf("task did not move to failed/: %v", err)
	}
	if failedTask.Status == "pr_opened" || failedTask.Status == "accepted" {
		t.Fatalf("forbidden-path acceptance was not blocked: status=%q", failedTask.Status)
	}
	foundForbiddenRisk := false
	for _, risk := range failedTask.Risks {
		if strings.Contains(risk.Detail, "task.scope.forbidden_paths") {
			foundForbiddenRisk = true
			break
		}
	}
	if !foundForbiddenRisk {
		t.Fatalf("expected forbidden-path risk in failed task:\n%#v", failedTask.Risks)
	}
}

// TestRunOnceReviewStagingFailureRecordsAttemptErrorBeforeSupervisor pins AC6:
// when the review-time `git add -A` step fails, the attempt must surface a
// staging-related attempt error (phase=review_staging, kind=review_staging_failed)
// and the supervisor must not be invoked with an empty diff. We swap out the
// staging seam used by runExecutorAttempt to inject a deterministic failure
// without depending on platform-specific git misbehavior.
func TestRunOnceReviewStagingFailureRecordsAttemptErrorBeforeSupervisor(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	promptPath, schemaPath := writeDaemonPromptFiles(t)
	claudeBin := writeFakeClaude(t, "echo change > daemon-output.txt\necho '{\"status\":\"completed\",\"summary\":\"done\",\"files_modified\":[\"daemon-output.txt\"],\"acceptance_criteria\":[{\"id\":\"AC1\",\"status\":\"satisfied\",\"evidence\":[\"diff\"],\"notes\":\"done\"}],\"verification\":[],\"scope_expansions\":[],\"decisions\":[],\"risks\":[]}'\n")
	taskPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, taskPath, repo)

	// Override the staging seam to capture invocation evidence under the
	// attempt dir (so file-based evidence still exists for review) and then
	// return an error that mimics a real `git add -A` failure.
	prev := stageExecutorOutput
	stageExecutorOutput = func(_ context.Context, _ Options, workDir, attemptDir string, _ []string) error {
		if err := os.MkdirAll(attemptDir, 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(attemptDir, "git_add_review_result.json"), []byte(`{"exit_code":128}`), 0o600); err != nil {
			return err
		}
		return fmt.Errorf("git add -A (review staging) failed: simulated index lock")
	}
	t.Cleanup(func() { stageExecutorOutput = prev })

	// AC6: a review-staging failure is a terminal attempt error. Run surfaces
	// the wrapped error to its caller (mirroring other daemon failure modes
	// like TestRunOnceFailsWhenPRBaseRefMissing) and moves the task to
	// tasks/failed with the staging-classified attempt error attached.
	runErr := Run(context.Background(), Options{
		Root:               root,
		SystemPromptFile:   promptPath,
		JSONSchemaFile:     schemaPath,
		Supervisor:         "claude",
		ClaudeBin:          claudeBin,
		Once:               true,
		MaxConcurrentTasks: 1,
	})
	if runErr == nil {
		t.Fatal("expected review-staging failure to be surfaced by Run")
	}
	if !strings.Contains(runErr.Error(), "review staging") && !strings.Contains(runErr.Error(), "git add") {
		t.Fatalf("Run error does not mention review staging: %v", runErr)
	}

	failedTask, err := task.Load(filepath.Join(root, "tasks", "failed", "task.yaml"))
	if err != nil {
		t.Fatalf("task did not move to failed/: %v", err)
	}
	if len(failedTask.Attempts) == 0 {
		t.Fatal("expected at least one recorded attempt")
	}
	last := failedTask.Attempts[len(failedTask.Attempts)-1]
	if last.Error == nil {
		t.Fatalf("expected attempt error, got nil")
	}
	if last.Error.Phase != "review_staging" || last.Error.Kind != "review_staging_failed" {
		t.Fatalf("attempt error got phase=%q kind=%q, want review_staging/review_staging_failed (%#v)", last.Error.Phase, last.Error.Kind, last.Error)
	}
	if !strings.Contains(last.Error.Message, "review staging") && !strings.Contains(last.Error.Message, "git add") {
		t.Fatalf("attempt error message does not mention staging: %q", last.Error.Message)
	}
	// AC6: when staging fails, supervisor must not have been invoked for that
	// attempt — i.e., no supervisor verdict file was written.
	assertGlobCount(t, filepath.Join(root, "runs", "*", "attempt-1", "supervisor_verdict.json"), 0)
}

// TestRunOnceReviewStagingExcludesNonCommittedInputFromAttemptDiff pins the
// review-time scope rule the supervisor flagged on attempt 3: review-time
// staging/evidence must be constrained to executor-produced changes. When a
// commit:false task input file is materialized in the worktree alongside a
// separate, executor-created untracked output, the attempt diff.patch (which
// is the same diff Galley hands to the supervisor) must include the
// executor's output and must NOT include the commit:false destination — even
// though `git add -A` would otherwise pick it up.
func TestRunOnceReviewStagingExcludesNonCommittedInputFromAttemptDiff(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	promptPath, schemaPath := writeDaemonPromptFiles(t)
	inputPath := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(inputPath, []byte("design note from plan\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Fake executor writes a separate untracked output and does NOT call
	// `git add`. The commit:false input file is placed by Galley earlier in
	// preparation; the executor never touches it.
	claudeBin := writeFakeClaude(t, "echo executor-output > daemon-output.txt\necho '{\"status\":\"completed\",\"summary\":\"done\",\"files_modified\":[\"daemon-output.txt\"],\"acceptance_criteria\":[{\"id\":\"AC1\",\"status\":\"satisfied\",\"evidence\":[\"diff\"],\"notes\":\"done\"}],\"verification\":[],\"scope_expansions\":[],\"decisions\":[],\"risks\":[]}'\n")
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

	// The diff.patch is the diff Galley hands to the supervisor as part of
	// the attempt evidence. It must reflect only the executor-produced change.
	diff := string(mustReadSingleGlob(t, filepath.Join(root, "runs", "*", "attempt-1", "diff.patch")))
	if !strings.Contains(diff, "daemon-output.txt") {
		t.Fatalf("attempt diff.patch missing executor output:\n%s", diff)
	}
	if !strings.Contains(diff, "executor-output") {
		t.Fatalf("attempt diff.patch missing executor content:\n%s", diff)
	}
	if strings.Contains(diff, "docs/plan.md") {
		t.Fatalf("commit:false input destination leaked into attempt diff.patch:\n%s", diff)
	}
	if strings.Contains(diff, "design note from plan") {
		t.Fatalf("commit:false input content leaked into attempt diff.patch:\n%s", diff)
	}

	// Defense in depth: the snapshot Galley writes as git_status.json carries
	// the staged_diff, unstaged_diff, and unioned diff fields used to populate
	// the supervisor Evidence.Diff. The status_porcelain field legitimately
	// reports untracked files (harmless because Evidence.Diff is derived from
	// the diff fields, not status), so the assertion targets the diff fields
	// only.
	statusBytes := mustReadSingleGlob(t, filepath.Join(root, "runs", "*", "attempt-1", "git_status.json"))
	for _, field := range []string{`"staged_diff"`, `"unstaged_diff"`, `"diff"`} {
		val := extractJSONStringField(string(statusBytes), field)
		if strings.Contains(val, "docs/plan.md") || strings.Contains(val, "design note from plan") {
			t.Fatalf("commit:false input leaked into snapshot %s field: %q", field, val)
		}
	}
}

// TestRunOnceReviewStagingDoesNotPresentContextInputAsSubmittedDiff pins the
// negative adjacent case: when the executor creates NO real change but a
// commit:false input file is present in the worktree, Galley must not let
// the context-only input show up as a submitted diff to the supervisor.
// In other words, the attempt diff.patch must be empty of any reference to
// the commit:false destination — the worktree change is context, not work.
func TestRunOnceReviewStagingDoesNotPresentContextInputAsSubmittedDiff(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	promptPath, schemaPath := writeDaemonPromptFiles(t)
	inputPath := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(inputPath, []byte("design note from plan\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Fake executor does not modify any file; it only emits a result JSON.
	claudeBin := writeFakeClaude(t, "echo '{\"status\":\"completed\",\"summary\":\"done\",\"files_modified\":[],\"acceptance_criteria\":[{\"id\":\"AC1\",\"status\":\"satisfied\",\"evidence\":[\"no diff\"],\"notes\":\"done\"}],\"verification\":[],\"scope_expansions\":[],\"decisions\":[],\"risks\":[]}'\n")
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

	// The submitted diff (== Evidence.Diff handed to the supervisor) must not
	// reference the context input — even though that input is physically in
	// the worktree. The diff.patch may be empty (no executor change), which
	// is the correct submitted-diff representation for "no work was done".
	diff := string(mustReadSingleGlob(t, filepath.Join(root, "runs", "*", "attempt-1", "diff.patch")))
	if strings.Contains(diff, "docs/plan.md") {
		t.Fatalf("commit:false context input presented as submitted diff:\n%s", diff)
	}
	if strings.Contains(diff, "design note from plan") {
		t.Fatalf("commit:false context input content presented as submitted diff:\n%s", diff)
	}
}

// TestNonCommittedInputDestinations documents the helper used by the daemon
// loop to derive the review-time exclude list from a task's input files. The
// helper trims, skips empty destinations, and ignores commit:true entries.
func TestNonCommittedInputDestinations(t *testing.T) {
	files := []task.InputFile{
		{Source: "/tmp/a", Destination: "docs/a.md", Commit: false},
		{Source: "/tmp/b", Destination: " docs/b.md ", Commit: false},
		{Source: "/tmp/c", Destination: "docs/c.md", Commit: true},
		{Source: "/tmp/d", Destination: "", Commit: false},
	}
	got := nonCommittedInputDestinations(files)
	want := []string{"docs/a.md", "docs/b.md"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got[%d]=%q want %q", i, got[i], want[i])
		}
	}
}

// extractJSONStringField returns the (still-escaped) string value of the
// supplied top-level JSON object key from a raw JSON object string. The key
// argument must include its surrounding quotes (e.g. `"diff"`). The helper
// keeps review-staging diff assertions readable without taking a dependency
// on the workspace package's internal types and without decoding the entire
// snapshot.
func extractJSONStringField(s, key string) string {
	idx := strings.Index(s, key+":\"")
	if idx < 0 {
		return ""
	}
	rest := s[idx+len(key)+2:]
	end := 0
	for end < len(rest) {
		if rest[end] == '\\' && end+1 < len(rest) {
			end += 2
			continue
		}
		if rest[end] == '"' {
			break
		}
		end++
	}
	return rest[:end]
}

// TestReviewStagingErrorClassification documents the helper used by the
// daemon loop to distinguish a review-time staging failure from a generic
// executor failure (AC6).
func TestReviewStagingErrorClassification(t *testing.T) {
	wrapped := &reviewStagingError{Err: errors.New("boom")}
	if got, ok := asReviewStagingError(wrapped); !ok || got != wrapped {
		t.Fatalf("expected reviewStagingError to be recognized directly")
	}
	nested := fmt.Errorf("wrapped: %w", wrapped)
	if _, ok := asReviewStagingError(nested); !ok {
		t.Fatalf("expected wrapped reviewStagingError to be unwrappable")
	}
	if _, ok := asReviewStagingError(errors.New("plain")); ok {
		t.Fatalf("plain errors must not be classified as review staging failures")
	}
}
