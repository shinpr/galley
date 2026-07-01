package daemon

// Acceptance skeleton for issue 70 (decouple PR comment polling from execution).
//
// AC1: "While a normal daemon is running a long executor attempt, PR comment
// polling shall continue on the configured poll interval without waiting for
// that executor attempt to finish."
//
// Behavior under test (trigger -> process -> observable result):
//   - Trigger: a normal (non --once) daemon claims a queued task whose executor
//     attempt blocks for longer than several poll intervals, while an actionable
//     open-PR task exists in tasks/done.
//   - Process: the refactored normal-daemon scheduler runs the maintenance
//     runner (PR comment polling + cleanup) on its own ticker, independent of
//     the long-running execution runner.
//   - Observable result: `gh api .../comments` polling for the done open-PR task
//     runs (and repeats on the poll interval) while the executor attempt is still
//     in progress, instead of being blocked until the executor exits.
//
// @lane: integration
// @category: core-functionality
// @dependency: daemon scheduler, queue, fake executor (claude), fake gh
// @complexity: high
// @roi: score=100 business_value=10 user_frequency=10 legal=false defect_detection=10
//   (primary acceptance behavior of the feature; currently unprotected because the
//   existing daemon cycle runs polling, cleanup, then execution serially and
//   processAvailable waits on the executor goroutine before the next cycle.)
// @timing: implement alongside the daemon scheduler refactor.
//
// Implementation notes for the executor:
//   - There is no existing test that proves polling is independent of execution;
//     this skeleton must be completed, not deleted.
//   - Use a cancelable context and a short PollInterval so the maintenance runner
//     ticks several times during the blocking executor attempt.
//   - The fake gh appends each comment-poll invocation to pollLog; assert at
//     least two poll invocations are recorded WHILE executorStarted exists and
//     before the executor attempt has been allowed to finish.
//   - Replace the explicit t.Fatal placeholder with the act + assert sequence.

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shinpr/galley/internal/queue"
	"github.com/shinpr/galley/internal/task"
)

func TestNormalDaemonPollsPRCommentsWhileExecutorAttemptIsRunning(t *testing.T) {
	// Arrange: Galley root + source repo.
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	runDaemonGit(t, repo, "branch", "-M", "main")
	if err := queue.EnsureLayout(root); err != nil {
		t.Fatal(err)
	}
	promptPath, schemaPath := writeDaemonPromptFiles(t)

	// Arrange: a blocking executor for the queued task. The marker file proves
	// the executor attempt started; the long sleep keeps it running across
	// several poll intervals so independent polling is observable.
	executorStarted := filepath.Join(t.TempDir(), "executor-started")
	claudeBin := writeFakeClaude(t, "touch "+executorStarted+"\nsleep 5\n"+
		"echo change > daemon-output.txt\n"+
		"echo '{\"status\":\"completed\",\"summary\":\"done\",\"files_modified\":[\"daemon-output.txt\"],\"acceptance_criteria\":[{\"id\":\"AC1\",\"status\":\"satisfied\",\"evidence\":[\"diff\"],\"notes\":\"done\"}],\"verification\":[],\"scope_expansions\":[],\"decisions\":[],\"risks\":[]}'\n")
	writeDaemonTask(t, filepath.Join(root, "tasks", "queued", "blocking.yaml"), repo)

	// Arrange: an actionable open-PR task in tasks/done whose PR comments must be
	// polled while the executor above is still blocked.
	writeDaemonEnvironmentProfile(t, root, repo, true, false)
	donePath := filepath.Join(root, "tasks", "done", "open-pr.yaml")
	writeDaemonTask(t, donePath, repo)
	doneTask, err := task.Load(donePath)
	if err != nil {
		t.Fatal(err)
	}
	doneTask.Status = "pr_opened"
	doneTask.PR.URL = "https://github.com/example/galley/pull/123"
	doneTask.PR.Status = "open"
	doneTask.PR.AuthorLogin = "author"
	if err := task.Save(donePath, doneTask); err != nil {
		t.Fatal(err)
	}

	// Arrange: a fake gh that records each comment-poll invocation so the test
	// can prove polling runs (and repeats) before the executor exits.
	pollLog := filepath.Join(t.TempDir(), "gh-poll.log")
	ghBin := writeFakeCommand(t, "gh", `if [ "$1" != "api" ]; then
echo unexpected-gh "$@" >&2
exit 1
fi
case "$2" in
repos/example/galley/issues/123/comments)
echo poll >> `+pollLog+`
echo '[[]]'
;;
repos/example/galley/pulls/123)
echo '{"state":"open","merged":false}'
;;
*)
echo unexpected-gh-api "$@" >&2
exit 1
;;
esac
`)

	// Act: start a normal (Once=false) daemon with a short PollInterval so the
	// maintenance runner ticks several times during the 5s blocking executor
	// attempt claimed from tasks/queued.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{
			Root:               root,
			SystemPromptFile:   promptPath,
			JSONSchemaFile:     schemaPath,
			Once:               false,
			MaxConcurrentTasks: 1,
			PollInterval:       20 * time.Millisecond,
			ClaimTTL:           time.Hour,
			ClaudeBin:          claudeBin,
			GHBin:              ghBin,
			Supervisor:         "claude",
		})
	}()

	// The executor-start marker proves the long executor attempt is in progress.
	waitForFileOrDone(t, executorStarted, done)

	// Assert: PR comment polling runs and repeats while the executor attempt is
	// still blocked. countPolls reads the recording fake gh log; the maintenance
	// runner must record >= 2 poll invocations before the 5s executor attempt is
	// released, proving polling does not wait for the executor to finish.
	countPolls := func() int {
		data, err := os.ReadFile(pollLog)
		if err != nil {
			if os.IsNotExist(err) {
				return 0
			}
			t.Fatal(err)
		}
		return bytes.Count(data, []byte("poll\n"))
	}
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for countPolls() < 2 {
		select {
		case err := <-done:
			t.Fatalf("daemon returned before maintenance polled twice (polls=%d): %v", countPolls(), err)
		case <-deadline.C:
			t.Fatalf("timed out waiting for >= 2 PR comment polls while executor attempt blocked (polls=%d)", countPolls())
		case <-tick.C:
		}
	}
	// The executor attempt must still be running: its start marker is present and
	// the 5s sleep has not been released, so the >= 2 polls above happened
	// independently of the execution runner rather than after it finished.
	if _, err := os.Stat(executorStarted); err != nil {
		t.Fatalf("executor start marker missing; executor attempt may have already finished: %v", err)
	}

	// Cancel and confirm the daemon shuts down cleanly once the in-flight
	// executor attempt drains.
	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("daemon shutdown returned unexpected error: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("daemon did not shut down after context cancel")
	}
}
