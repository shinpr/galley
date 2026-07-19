package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shinpr/galley/internal/queue"
	"github.com/shinpr/galley/internal/task"
)

func TestRunOnceDrainsQueue(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo1 := initDaemonGitRepo(t)
	repo2 := initDaemonGitRepo(t)
	promptPath, schemaPath := writeDaemonPromptFiles(t)
	claudeBin := writeFakeClaude(t, "echo change > daemon-output.txt\necho '{\"status\":\"completed\",\"summary\":\"done\",\"files_modified\":[\"daemon-output.txt\"],\"acceptance_criteria\":[{\"id\":\"AC1\",\"status\":\"satisfied\",\"evidence\":[\"diff\"],\"notes\":\"done\"}],\"verification\":[],\"scope_expansions\":[],\"decisions\":[],\"risks\":[]}'\n")
	queueDir := filepath.Join(root, "tasks", "queued")
	if err := os.MkdirAll(queueDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, filepath.Join(queueDir, "task-1.yaml"), repo1)
	writeDaemonTask(t, filepath.Join(queueDir, "task-2.yaml"), repo2)

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
	assertGlobCount(t, filepath.Join(root, "tasks", "queued", "*.yaml"), 0)
	assertGlobCount(t, filepath.Join(root, "tasks", "done", "*.yaml"), 2)
}

func TestRunOnceContinuesAfterTaskFailure(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	promptPath, schemaPath := writeDaemonPromptFiles(t)
	claudeBin := writeFakeClaude(t, "echo change > daemon-output.txt\necho '{\"status\":\"completed\",\"summary\":\"done\",\"files_modified\":[\"daemon-output.txt\"],\"acceptance_criteria\":[{\"id\":\"AC1\",\"status\":\"satisfied\",\"evidence\":[\"diff\"],\"notes\":\"done\"}],\"verification\":[],\"scope_expansions\":[],\"decisions\":[],\"risks\":[]}'\n")
	queueDir := filepath.Join(root, "tasks", "queued")
	if err := os.MkdirAll(queueDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(queueDir, "bad.yaml"), []byte("id: broken\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, filepath.Join(queueDir, "good.yaml"), repo)

	err := runTestDaemon(context.Background(), Options{
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

	err = runTestDaemon(context.Background(), Options{
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

	processed, err := processAvailableForTest(context.Background(), Options{Root: root, MaxConcurrentTasks: 1, ClaimTTL: time.Hour}.withDefaults())
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
	claudeBin := writeFakeClaude(t, "echo change > daemon-output.txt\necho '{\"status\":\"completed\",\"summary\":\"done\",\"files_modified\":[\"daemon-output.txt\"],\"acceptance_criteria\":[{\"id\":\"AC1\",\"status\":\"satisfied\",\"evidence\":[\"diff\"],\"notes\":\"done\"}],\"verification\":[],\"scope_expansions\":[],\"decisions\":[],\"risks\":[]}'\n")
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

	processed, err := processAvailableForTest(context.Background(), Options{
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
	claudeBin := writeFakeClaude(t, "echo change > daemon-output.txt\necho '{\"status\":\"completed\",\"summary\":\"done\",\"files_modified\":[\"daemon-output.txt\"],\"acceptance_criteria\":[{\"id\":\"AC1\",\"status\":\"satisfied\",\"evidence\":[\"diff\"],\"notes\":\"done\"}],\"verification\":[],\"scope_expansions\":[],\"decisions\":[],\"risks\":[]}'\n")
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

	processed, err := processAvailableForTest(context.Background(), Options{
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
	claudeBin := writeFakeClaude(t, "echo change > daemon-output.txt\necho '{\"status\":\"completed\",\"summary\":\"done\",\"files_modified\":[\"daemon-output.txt\"],\"acceptance_criteria\":[{\"id\":\"AC1\",\"status\":\"satisfied\",\"evidence\":[\"diff\"],\"notes\":\"done\"}],\"verification\":[],\"scope_expansions\":[],\"decisions\":[],\"risks\":[]}'\n")
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

	processed, err := processAvailableForTest(context.Background(), Options{
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
		_, err := processAvailableForTest(context.Background(), Options{
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
	claudeBin := writeFakeClaude(t, "touch "+started+"\nsleep 0.05\necho change > daemon-output.txt\necho '{\"status\":\"completed\",\"summary\":\"done\",\"files_modified\":[\"daemon-output.txt\"],\"acceptance_criteria\":[{\"id\":\"AC1\",\"status\":\"satisfied\",\"evidence\":[\"diff\"],\"notes\":\"done\"}],\"verification\":[],\"scope_expansions\":[],\"decisions\":[],\"risks\":[]}'\n")
	queueDir := filepath.Join(root, "tasks", "queued")
	if err := os.MkdirAll(queueDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, filepath.Join(queueDir, "task.yaml"), repo)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	processed, err := processAvailableForTest(ctx, Options{
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
		_, err := processAvailableForTest(ctx, Options{
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
	waitForFileOrDone(t, started, done)
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
	claudeBin := writeFakeClaude(t, "echo attempt >> "+attemptLog+"\nsleep 0.05\necho change >> daemon-output.txt\necho '{\"status\":\"completed\",\"summary\":\"done\",\"files_modified\":[\"daemon-output.txt\"],\"acceptance_criteria\":[{\"id\":\"AC1\",\"status\":\"satisfied\",\"evidence\":[\"diff\"],\"notes\":\"done\"}],\"verification\":[],\"scope_expansions\":[],\"decisions\":[],\"risks\":[]}'\n")
	codexBin := writeFakeCodexSupervisor(t, `{"status":"needs_revision","summary":"codex wants retry","acceptance_passes":[],"quality_passes":[],"findings":["retry"],"discussion_items":[]}`)
	queueDir := filepath.Join(root, "tasks", "queued")
	if err := os.MkdirAll(queueDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, filepath.Join(queueDir, "task.yaml"), repo)
	setLoopBudget(t, filepath.Join(queueDir, "task.yaml"), 2)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := processAvailableForTest(ctx, Options{
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
	waitForFileOrDone(t, attemptLog, done)
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
	if failedTask.Status != "failed" {
		t.Fatalf("status got %q", failedTask.Status)
	}
	if len(failedTask.Risks) == 0 || !strings.Contains(failedTask.Risks[len(failedTask.Risks)-1].ID, "shutdown-") {
		t.Fatalf("shutdown risk missing: %#v", failedTask.Risks)
	}
	assertSupervisorRevisionState(t, failedTask, "supervisor-attempt-1-finding-1", "retry")
}

func TestInterruptedRecoveryPreservesPendingSupervisorRevision(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	promptPath, schemaPath := writeDaemonPromptFiles(t)
	markers := t.TempDir()
	firstAttempt := filepath.Join(markers, "first-attempt")
	secondAttempt := filepath.Join(markers, "second-attempt")
	releaseSecondAttempt := filepath.Join(markers, "release-second-attempt")
	claudeBin := writeFakeClaude(t, "if [ -f "+shellPath(firstAttempt)+" ]; then\n"+
		"  touch "+shellPath(secondAttempt)+"\n"+
		"  while [ ! -f "+shellPath(releaseSecondAttempt)+" ]; do sleep 0.01; done\n"+
		"else\n"+
		"  touch "+shellPath(firstAttempt)+"\n"+
		"fi\n"+
		"echo change >> daemon-output.txt\n"+
		"echo '{\"status\":\"completed\",\"summary\":\"done\",\"files_modified\":[\"daemon-output.txt\"],\"acceptance_criteria\":[{\"id\":\"AC1\",\"status\":\"satisfied\",\"evidence\":[\"diff\"],\"notes\":\"done\"}],\"verification\":[],\"scope_expansions\":[],\"decisions\":[],\"risks\":[]}'\n")
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
request="$(cat)"
verdict='{"status":"needs_revision","summary":"codex wants retry","acceptance_passes":[],"quality_passes":[],"findings":["persist this revision"],"discussion_items":[]}'
printf '%s\n' "$verdict" > "$out"
printf '%s\n' '{"event":"done"}'
`)
	queuedPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(queuedPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, queuedPath, repo)
	setLoopBudget(t, queuedPath, 2)

	done := make(chan error, 1)
	go func() {
		_, err := processAvailableForTest(context.Background(), Options{
			Root:               root,
			SystemPromptFile:   promptPath,
			JSONSchemaFile:     schemaPath,
			ClaudeBin:          claudeBin,
			MaxConcurrentTasks: 1,
			ClaimTTL:           time.Hour,
			Supervisor:         "codex",
			CodexBin:           codexBin,
		}.withDefaults())
		done <- err
	}()
	defer func() { _ = os.WriteFile(releaseSecondAttempt, []byte("release"), 0o600) }()
	waitForFileOrDone(t, secondAttempt, done)

	runningTask, err := task.Load(filepath.Join(root, "tasks", "running", "task.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	assertSupervisorRevisionState(t, runningTask, "supervisor-attempt-1-finding-1", "persist this revision")

	recoveryRoot := filepath.Join(t.TempDir(), ".agent-workflow")
	if err := queue.EnsureLayout(recoveryRoot); err != nil {
		t.Fatal(err)
	}
	recoveryRunningPath := filepath.Join(recoveryRoot, "tasks", "running", "task.yaml")
	if err := task.Save(recoveryRunningPath, runningTask); err != nil {
		t.Fatal(err)
	}
	if err := queue.WriteOwner(recoveryRunningPath, queue.Owner{PID: 424242, RecordedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	if err := queue.RecoverInterruptedRunning(recoveryRoot, func(queue.Owner) (bool, error) { return false, nil }); err != nil {
		t.Fatal(err)
	}
	recoveredTask, err := task.Load(filepath.Join(recoveryRoot, "tasks", "queued", "task.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	assertSupervisorRevisionState(t, recoveredTask, "supervisor-attempt-1-finding-1", "persist this revision")

	if err := os.WriteFile(releaseSecondAttempt, []byte("release"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
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

	err := runTestDaemon(context.Background(), Options{
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
