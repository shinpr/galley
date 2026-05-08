package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shinpr/galley/internal/queue"
	"github.com/shinpr/galley/internal/task"
	"github.com/shinpr/galley/internal/workspace"
)

func TestRunOnceMovesTaskToDoneAndWritesRunEvidence(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	promptPath, schemaPath := writeDaemonPromptFiles(t)
	writeFakeClaude(t, "echo change > daemon-output.txt\necho '{\"status\":\"completed\",\"summary\":\"done\",\"files_modified\":[\"daemon-output.txt\"],\"acceptance_criteria\":[{\"id\":\"AC1\",\"status\":\"satisfied\",\"evidence\":[\"diff\"],\"notes\":\"done\"}],\"verification\":[],\"decisions\":[],\"risks\":[]}'\n")
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
	if len(doneTask.Verification.Commands) < 2 || doneTask.Verification.Commands[1].Status != "passed" {
		t.Fatalf("verification got %#v", doneTask.Verification.Commands)
	}
	assertGlobCount(t, filepath.Join(root, "runs", "*", "task.yaml"), 1)
	assertGlobCount(t, filepath.Join(root, "runs", "*", "workspace.json"), 1)
	assertGlobCount(t, filepath.Join(root, "runs", "*", "task.effective.yaml"), 1)
	assertGlobCount(t, filepath.Join(root, "runs", "*", "attempt-1", "command_plan.json"), 1)
	assertGlobCount(t, filepath.Join(root, "runs", "*", "attempt-1", "run_result.json"), 1)
	assertGlobCount(t, filepath.Join(root, "runs", "*", "attempt-1", "claude_result.json"), 1)
	assertGlobCount(t, filepath.Join(root, "runs", "*", "attempt-1", "supervisor_verdict.json"), 1)
	assertGlobCount(t, filepath.Join(root, "runs", "*", "attempt-1", "git_status.json"), 1)
	assertGlobCount(t, filepath.Join(root, "runs", "*", "attempt-1", "diff.patch"), 1)
	assertGlobCount(t, filepath.Join(root, "runs", "*", "attempt-1", "claude.stdout.jsonl"), 1)
}

func TestRunOnceUsesExternalSupervisorCommand(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	promptPath, schemaPath := writeDaemonPromptFiles(t)
	writeFakeClaude(t, "echo change > daemon-output.txt\necho '{\"status\":\"completed\",\"summary\":\"done\",\"files_modified\":[\"daemon-output.txt\"],\"acceptance_criteria\":[{\"id\":\"AC1\",\"status\":\"satisfied\",\"evidence\":[\"diff\"],\"notes\":\"done\"}],\"verification\":[],\"decisions\":[],\"risks\":[]}'\n")
	supervisorPath := filepath.Join(t.TempDir(), "supervisor")
	if err := os.WriteFile(supervisorPath, []byte("#!/bin/sh\ncat >/dev/null\necho '{\"status\":\"accepted\",\"summary\":\"external accepted\"}'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
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
		SupervisorCommand:  []string{supervisorPath},
	})
	if err != nil {
		t.Fatal(err)
	}
	doneTask, err := task.Load(filepath.Join(root, "tasks", "done", "task.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if doneTask.Attempts[0].SupervisorVerdict != "accepted" || !strings.Contains(doneTask.Attempts[0].Summary, "external accepted") {
		t.Fatalf("attempts got %#v", doneTask.Attempts)
	}
	assertGlobCount(t, filepath.Join(root, "runs", "*", "attempt-1", "external_supervisor_verdict.json"), 1)
}

func TestRunOnceRetriesCorrectiveWorkOrderUntilAccepted(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	promptPath, schemaPath := writeDaemonPromptFiles(t)
	writeFakeClaude(t, `if [ -f retry.marker ]; then
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

func TestRunOnceOpenPRCommitsPushesAndUpdatesTask(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	runDaemonGit(t, t.TempDir(), "init", "--bare", remote)
	runDaemonGit(t, repo, "remote", "add", "origin", remote)
	promptPath, schemaPath := writeDaemonPromptFiles(t)
	writeFakeClaude(t, "echo change > daemon-output.txt\necho '{\"status\":\"completed\",\"summary\":\"done\",\"files_modified\":[\"daemon-output.txt\"],\"acceptance_criteria\":[{\"id\":\"AC1\",\"status\":\"satisfied\",\"evidence\":[\"diff\"],\"notes\":\"done\"}],\"verification\":[],\"decisions\":[],\"risks\":[]}'\n")
	writeFakeCommand(t, "gh", "echo https://github.com/example/galley/pull/123\n")
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
		OpenPR:             true,
		PRBase:             "main",
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
	writeFakeClaude(t, "echo '{\"status\":\"hard_stop\",\"summary\":\"blocked\",\"files_modified\":[],\"acceptance_criteria\":[],\"verification\":[],\"decisions\":[],\"risks\":[],\"hard_stop\":{\"reason\":\"missing secret\",\"attempted\":[],\"needed_to_continue\":[\"secret\"]}}'\n")
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
	writeFakeClaude(t, "echo not-json\n")
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
	writeFakeClaude(t, "echo '{\"status\":\"completed_with_risks\",\"summary\":\"done\",\"files_modified\":[],\"acceptance_criteria\":[],\"verification\":[],\"decisions\":[],\"risks\":[{\"type\":\"partial_verification\",\"detail\":\"tests skipped\",\"mitigation\":\"raw logs saved\",\"needs_human_review\":true}]}'\n")
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
	writeFakeClaude(t, "echo '{\"status\":\"completed\",\"summary\":\"done\",\"files_modified\":[],\"acceptance_criteria\":[],\"verification\":[],\"decisions\":[],\"risks\":[]}'\n")
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
	writeFakeClaude(t, "echo '{\"status\":\"completed\",\"summary\":\"done\",\"files_modified\":[],\"acceptance_criteria\":[{\"id\":\"AC1\",\"status\":\"satisfied\",\"evidence\":[\"claimed\"],\"notes\":\"claimed\"}],\"verification\":[],\"decisions\":[],\"risks\":[]}'\n")
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
	writeFakeClaude(t, "echo change > daemon-output.txt\necho '{\"status\":\"completed\",\"summary\":\"done\",\"files_modified\":[\"daemon-output.txt\"],\"acceptance_criteria\":[{\"id\":\"AC1\",\"status\":\"satisfied\",\"evidence\":[\"diff\"],\"notes\":\"done\"}],\"verification\":[],\"decisions\":[],\"risks\":[]}'\n")
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
	writeFakeClaude(t, "echo change > daemon-output.txt\necho '{\"status\":\"completed\",\"summary\":\"done\",\"files_modified\":[\"daemon-output.txt\"],\"acceptance_criteria\":[{\"id\":\"AC1\",\"status\":\"satisfied\",\"evidence\":[\"diff\"],\"notes\":\"done\"}],\"verification\":[],\"decisions\":[],\"risks\":[]}'\n")
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
	if err := os.WriteFile(runningPath, []byte("running"), 0o600); err != nil {
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
	writeFakeClaude(t, "echo change > daemon-output.txt\necho '{\"status\":\"completed\",\"summary\":\"done\",\"files_modified\":[\"daemon-output.txt\"],\"acceptance_criteria\":[{\"id\":\"AC1\",\"status\":\"satisfied\",\"evidence\":[\"diff\"],\"notes\":\"done\"}],\"verification\":[],\"decisions\":[],\"risks\":[]}'\n")
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
	if err := os.WriteFile(filepath.Join(runningDir, "a-conflict.yaml"), []byte("running"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, filepath.Join(queueDir, "b-good.yaml"), repo)

	processed, err := processAvailable(context.Background(), Options{
		Root:               root,
		SystemPromptFile:   promptPath,
		JSONSchemaFile:     schemaPath,
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
	writeFakeClaude(t, "echo change > daemon-output.txt\necho '{\"status\":\"completed\",\"summary\":\"done\",\"files_modified\":[\"daemon-output.txt\"],\"acceptance_criteria\":[{\"id\":\"AC1\",\"status\":\"satisfied\",\"evidence\":[\"diff\"],\"notes\":\"done\"}],\"verification\":[],\"decisions\":[],\"risks\":[]}'\n")
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
	writeFakeClaude(t, "echo change > daemon-output.txt\necho '{\"status\":\"completed\",\"summary\":\"done\",\"files_modified\":[\"daemon-output.txt\"],\"acceptance_criteria\":[{\"id\":\"AC1\",\"status\":\"satisfied\",\"evidence\":[\"diff\"],\"notes\":\"done\"}],\"verification\":[],\"decisions\":[],\"risks\":[]}'\n")
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
	writeFakeClaude(t, "sleep 0.05\necho change > daemon-output.txt\necho '{\"status\":\"completed\",\"summary\":\"done\",\"files_modified\":[\"daemon-output.txt\"],\"acceptance_criteria\":[{\"id\":\"AC1\",\"status\":\"satisfied\",\"evidence\":[\"diff\"],\"notes\":\"done\"}],\"verification\":[],\"decisions\":[],\"risks\":[]}'\n")
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
			MaxConcurrentTasks: 1,
			ClaimTTL:           time.Hour,
			ShutdownTimeout:    time.Second,
		}.withDefaults())
		done <- err
	}()
	time.Sleep(10 * time.Millisecond)
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
	writeFakeClaude(t, "echo attempt >> "+attemptLog+"\nsleep 0.05\necho change >> daemon-output.txt\necho '{\"status\":\"completed\",\"summary\":\"done\",\"files_modified\":[\"daemon-output.txt\"],\"acceptance_criteria\":[{\"id\":\"AC1\",\"status\":\"satisfied\",\"evidence\":[\"diff\"],\"notes\":\"done\"}],\"verification\":[],\"decisions\":[],\"risks\":[]}'\n")
	supervisorPath := filepath.Join(t.TempDir(), "supervisor")
	if err := os.WriteFile(supervisorPath, []byte("#!/bin/sh\ncat >/dev/null\necho '{\"status\":\"needs_revision\",\"summary\":\"external wants retry\",\"acceptance_gaps\":[\"retry\"],\"quality_findings\":[],\"next_work_order\":\"try again\"}'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
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
			MaxConcurrentTasks: 1,
			ClaimTTL:           time.Hour,
			ShutdownTimeout:    time.Second,
			SupervisorCommand:  []string{supervisorPath},
		}.withDefaults())
		done <- err
	}()
	time.Sleep(10 * time.Millisecond)
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
	if err := os.WriteFile(filepath.Join(runningDir, "task.yaml"), []byte("running"), 0o600); err != nil {
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
	writeFakeCommand(t, "gh", "echo '{\"state\":\"open\",\"merged\":false}'\n")

	if err := cleanupWorktrees(context.Background(), Options{Root: root}.withDefaults()); err != nil {
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
	writeFakeCommand(t, "gh", "echo '{\"state\":\"closed\",\"merged\":true}'\n")

	if err := cleanupWorktrees(context.Background(), Options{Root: root}.withDefaults()); err != nil {
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
	if len(reloaded.Attempts) == 0 || reloaded.Attempts[len(reloaded.Attempts)-1].SupervisorVerdict != "cleanup" {
		t.Fatalf("cleanup attempt missing: %#v", reloaded.Attempts)
	}
}

func TestCleanupWorktreesSkipsDirtyClosedPRWorktree(t *testing.T) {
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
	writeFakeCommand(t, "gh", "echo '{\"state\":\"closed\",\"merged\":false}'\n")

	if err := cleanupWorktrees(context.Background(), Options{Root: root}.withDefaults()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(worktreePath); err != nil {
		t.Fatalf("dirty worktree should remain: %v", err)
	}
	reloaded, err := task.Load(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.PR.Status != "closed" {
		t.Fatalf("pr status got %q", reloaded.PR.Status)
	}
	if !hasCleanupRisk(reloaded.Risks) {
		t.Fatalf("cleanup risk missing: %#v", reloaded.Risks)
	}
}

func writeFakeClaude(t *testing.T, body string) {
	t.Helper()
	writeFakeCommand(t, "claude", body)
}

func prepareDonePRTask(t *testing.T, taskPath, repo, prStatus string) (task.Task, string) {
	t.Helper()
	loaded, err := task.Load(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := workspace.Prepare(context.Background(), repo, loaded.Worktree)
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

func writeFakeCommand(t *testing.T, name, body string) {
	t.Helper()
	binDir := t.TempDir()
	commandPath := filepath.Join(binDir, name)
	if err := os.WriteFile(commandPath, []byte("#!/bin/sh\n"+body), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
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
	worktreePath := filepath.Join(filepath.Dir(repo), "worktrees", name)
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
  cwd: "` + repo + `"
  allowed_paths:
    - "."
  forbidden_paths: []
  permission: "safe-edit"
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
  path: "` + worktreePath + `"
supervisor:
  provider: "codex"
  mode: "review_and_repair"
  approval_required: true
  approval_status: "pending"
  review_iterations: 0
executor:
  cli: "claude"
  model: "opus"
  effort: "high"
  prompt_profile: "codexized-claude-executor-v1"
  prompt_mode: "replace"
  max_budget_usd: 0
  max_turns: 0
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

func setLoopBudget(t *testing.T, path string, count int) {
	t.Helper()
	loaded, err := task.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	loaded.ExecutionPolicy.LoopBudget.Count = count
	loaded.ExecutionPolicy.LoopBudget.Infinite = false
	loaded.ExecutionPolicy.LoopBudget.Set = true
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
