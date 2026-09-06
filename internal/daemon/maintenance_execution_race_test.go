package daemon

// Acceptance skeleton for issue 70 (decouple PR comment polling from execution).
//
// AC6: "While maintenance and execution run concurrently, requeue publication
// and queued-task claim shall remain race-free so a requeued task is neither
// lost nor double-claimed under the existing no-overwrite publication and claim
// boundary."
//
// Behavior under test (trigger -> process -> observable result):
//   - Trigger: the maintenance runner requeues an actionable open-PR task from
//     tasks/done (or tasks/failed) to tasks/queued at the same time the
//     execution runner scans/claims queued tasks.
//   - Process: requeue publishes the queued task via the existing no-overwrite
//     atomic write, and claim moves tasks/queued/<name>.yaml to
//     tasks/running/<name>.yaml via a no-overwrite rename.
//   - Observable result: the requeued task ends up in exactly one terminal
//     location (still queued, or claimed into running exactly once) and is never
//     lost or double-claimed, even under concurrent interleaving.
//
// @lane: integration
// @category: contract
// @dependency: queue (QueuedTasks/ClaimTask), task.Requeue, no-overwrite write/rename
// @complexity: high
// @roi: score=80 business_value=8 user_frequency=5 legal=false defect_detection=10
//   (the design's only new concurrency boundary; existing primitive tests cover
//   no-overwrite publication and claim in isolation, but not the concurrent
//   maintenance-requeue vs execution-claim interleaving introduced by the
//   independent maintenance scheduler.)
// @timing: implement alongside the daemon scheduler refactor.
//
// Implementation notes for the executor:
//   - This is a focused interleaving regression test, distinct from the existing
//     TestRequeueDoesNotOverwriteQueuedTask and TestClaimTaskDoesNotOverwriteRunningTask
//     primitive tests; complete it rather than relying on those.
//   - Drive requeue and claim from separate goroutines (optionally repeated /
//     looped or synchronized with a barrier) over the same task name, then make
//     a deterministic assertion about exactly-once placement.
//   - A double-claim or lost task must fail the assertion.

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/shinpr/galley/internal/queue"
	"github.com/shinpr/galley/internal/task"
)

func TestMaintenanceRequeueAndExecutionClaimStayRaceFree(t *testing.T) {
	// Arrange: Galley root + source repo + queue layout.
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	if err := queue.EnsureLayout(root); err != nil {
		t.Fatal(err)
	}

	// Arrange: an actionable open-PR task in tasks/done that the maintenance
	// runner will requeue from an authorized /galley comment requeue path.
	donePath := filepath.Join(root, "tasks", "done", "race.yaml")
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

	// Preserve the original done task bytes so each interleaving round can reset
	// to the same actionable open-PR state before requeue publication runs again.
	doneBytes, err := os.ReadFile(donePath)
	if err != nil {
		t.Fatal(err)
	}
	queuedGlob := filepath.Join(root, "tasks", "queued", "*.yaml")
	runningGlob := filepath.Join(root, "tasks", "running", "*.yaml")
	doneGlob := filepath.Join(root, "tasks", "done", "*.yaml")

	resetRound := func() {
		removeMatching(t, queuedGlob, runningGlob,
			filepath.Join(root, "tasks", "queued", "*.lock"),
			filepath.Join(root, "tasks", "running", "*.lock"))
		if writeErr := os.WriteFile(donePath, doneBytes, 0o600); writeErr != nil {
			t.Fatal(writeErr)
		}
	}

	// Act: drive maintenance requeue publication and execution claim concurrently
	// over many rounds. A shared start barrier releases both goroutines at once
	// so the no-overwrite publication (done -> queued) and the no-overwrite claim
	// (queued -> running) interleave under the race detector.
	const rounds = 80
	for round := 0; round < rounds; round++ {
		resetRound()

		start := make(chan struct{})
		errs := make(chan error, 2)
		var wg sync.WaitGroup
		wg.Add(2)

		// Maintenance publication goroutine: requeue the actionable done task.
		go func() {
			defer wg.Done()
			<-start
			if _, reqErr := task.Requeue(donePath, task.RequeueOptions{
				Reason:              "interleave round",
				ProcessedCommentIDs: []string{"1"},
			}); reqErr != nil {
				errs <- fmt.Errorf("requeue failed: %w", reqErr)
			}
		}()

		// Execution claim goroutine: scan queued tasks and claim them into running
		// via the no-overwrite rename, retrying briefly so it overlaps publication.
		go func() {
			defer wg.Done()
			<-start
			claimUntilClaimedOrDeadline(root, errs)
		}()

		close(start)
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatalf("round %d: %v", round, err)
			}
		}

		// Assert exactly-once placement: the requeued task lives in exactly one of
		// queued or running, is never double-claimed (never in both), and is never
		// lost. The source done task is removed exactly once when publication wins.
		assertExactlyOneLiveCopy(t, round, taskGlobs{Queued: queuedGlob, Running: runningGlob, Done: doneGlob})
	}

	// Final state assertions over the canonical no-overwrite boundary: after the
	// last round the task sits in exactly one terminal queue location.
	finalQueued := globForTest(t, queuedGlob)
	finalRunning := globForTest(t, runningGlob)
	finalDone := globForTest(t, doneGlob)
	if total := len(finalQueued) + len(finalRunning) + len(finalDone); total != 1 {
		t.Fatalf("final placement: expected exactly one live copy, got %d (queued=%v running=%v done=%v)",
			total, finalQueued, finalRunning, finalDone)
	}
	if len(finalDone) != 0 {
		t.Fatalf("final placement: requeued task source remained in done: %v", finalDone)
	}
	assertGlobCount(t, runningGlob, len(finalRunning))
}

func globForTest(t *testing.T, pattern string) []string {
	t.Helper()
	matches, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatal(err)
	}
	return matches
}

func removeMatching(t *testing.T, patterns ...string) {
	t.Helper()
	for _, pattern := range patterns {
		for _, m := range globForTest(t, pattern) {
			if rmErr := os.Remove(m); rmErr != nil && !os.IsNotExist(rmErr) {
				t.Fatal(rmErr)
			}
		}
	}
}

// claimUntilClaimedOrDeadline claims queued tasks via the no-overwrite rename,
// retrying briefly so it overlaps publication; a conflict just continues the scan.
func claimUntilClaimedOrDeadline(root string, errs chan<- error) {
	deadline := time.Now().Add(500 * time.Millisecond)
	for {
		queued, qErr := queue.QueuedTasks(root)
		if qErr != nil {
			errs <- fmt.Errorf("QueuedTasks failed: %w", qErr)
			return
		}
		claimed := false
		for _, qPath := range queued {
			if _, claimErr := queue.ClaimTask(root, qPath); claimErr == nil {
				claimed = true
			}
		}
		if claimed || time.Now().After(deadline) {
			return
		}
	}
}

// taskGlobs are the queue-location globs one interleaving round asserts over.
type taskGlobs struct {
	Queued  string
	Running string
	Done    string
}

// assertExactlyOneLiveCopy checks exactly-once placement: the task lives in one
// of queued or running, is never double-claimed or lost, and leaves done once.
func assertExactlyOneLiveCopy(t *testing.T, round int, globs taskGlobs) {
	t.Helper()
	queued := globForTest(t, globs.Queued)
	running := globForTest(t, globs.Running)
	done := globForTest(t, globs.Done)
	if live := len(queued) + len(running) + len(done); live != 1 {
		t.Fatalf("round %d: expected exactly one live copy of the task, got %d (queued=%v running=%v done=%v)",
			round, live, queued, running, done)
	}
	if len(done) != 0 {
		t.Fatalf("round %d: requeued task source remained in done after publication: %v", round, done)
	}
	if len(running) > 1 {
		t.Fatalf("round %d: task was double-claimed into running: %v", round, running)
	}
	if len(queued) == 1 && len(running) == 1 {
		t.Fatalf("round %d: task present in both queued and running (lost no-overwrite boundary)", round)
	}
}
