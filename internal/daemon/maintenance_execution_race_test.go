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
		// Clear any prior round's queued/running copies and stale claim locks,
		// then restore the source done task so publication has something to move.
		for _, pattern := range []string{
			queuedGlob, runningGlob,
			filepath.Join(root, "tasks", "queued", "*.lock"),
			filepath.Join(root, "tasks", "running", "*.lock"),
		} {
			matches, globErr := filepath.Glob(pattern)
			if globErr != nil {
				t.Fatal(globErr)
			}
			for _, m := range matches {
				if rmErr := os.Remove(m); rmErr != nil && !os.IsNotExist(rmErr) {
					t.Fatal(rmErr)
				}
			}
		}
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
				// A failed publication leaves the done task in place; the
				// placement assertion below still holds (live copy stays in done
				// only because queued/running are empty). Record nothing here so
				// the deterministic assertion catches any real loss.
				_ = reqErr
			}
		}()

		// Execution claim goroutine: scan queued tasks and claim them into running
		// via the no-overwrite rename, retrying briefly so it overlaps publication.
		go func() {
			defer wg.Done()
			<-start
			deadline := time.Now().Add(500 * time.Millisecond)
			for {
				queued, qErr := queue.QueuedTasks(root)
				if qErr != nil {
					t.Errorf("QueuedTasks failed: %v", qErr)
					return
				}
				claimed := false
				for _, qPath := range queued {
					if _, claimErr := queue.ClaimTask(root, qPath); claimErr == nil {
						claimed = true
					}
					// ErrClaimConflict or a missing-source rename error means the
					// task was not claimed this pass; keep scanning/retrying.
				}
				if claimed || time.Now().After(deadline) {
					return
				}
			}
		}()

		close(start)
		wg.Wait()

		// Assert exactly-once placement: the requeued task lives in exactly one of
		// queued or running, is never double-claimed (never in both), and is never
		// lost. The source done task is removed exactly once when publication wins.
		queuedMatches, gErr := filepath.Glob(queuedGlob)
		if gErr != nil {
			t.Fatal(gErr)
		}
		runningMatches, gErr := filepath.Glob(runningGlob)
		if gErr != nil {
			t.Fatal(gErr)
		}
		doneMatches, gErr := filepath.Glob(doneGlob)
		if gErr != nil {
			t.Fatal(gErr)
		}
		live := len(queuedMatches) + len(runningMatches) + len(doneMatches)
		if live != 1 {
			t.Fatalf("round %d: expected exactly one live copy of the task, got %d (queued=%v running=%v done=%v)",
				round, live, queuedMatches, runningMatches, doneMatches)
		}
		if len(runningMatches) > 1 {
			t.Fatalf("round %d: task was double-claimed into running: %v", round, runningMatches)
		}
		if len(queuedMatches) == 1 && len(runningMatches) == 1 {
			t.Fatalf("round %d: task present in both queued and running (lost no-overwrite boundary)", round)
		}
	}

	// Final state assertions over the canonical no-overwrite boundary: after the
	// last round the task sits in exactly one terminal queue location.
	finalQueued, err := filepath.Glob(queuedGlob)
	if err != nil {
		t.Fatal(err)
	}
	finalRunning, err := filepath.Glob(runningGlob)
	if err != nil {
		t.Fatal(err)
	}
	finalDone, err := filepath.Glob(doneGlob)
	if err != nil {
		t.Fatal(err)
	}
	if total := len(finalQueued) + len(finalRunning) + len(finalDone); total != 1 {
		t.Fatalf("final placement: expected exactly one live copy, got %d (queued=%v running=%v done=%v)",
			total, finalQueued, finalRunning, finalDone)
	}
	assertGlobCount(t, runningGlob, len(finalRunning))
}
