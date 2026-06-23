package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/shinpr/galley/internal/notify"
	"github.com/shinpr/galley/internal/task"
)

// notifyDeliveryTracking, when non-nil, lets tests await the asynchronous
// notification goroutines started by notifyTerminalPublication. It is nil in
// production on purpose: notification delivery is fire-and-forget so the daemon
// worker goroutine and the next daemon iteration never wait for (or are blocked
// by) a slow or stuck notification command. See trackNotifications in the test
// file for the wiring.
var notifyDeliveryTracking *sync.WaitGroup

// notifyTerminalPublication starts the opt-in, best-effort notification command
// hook for a claimed task that has finished processing. It is invoked from a
// defer in processClaimedTask, so by the time it runs every terminal
// publication has already happened through taskstate.Move / taskstate.FailMove.
//
// Delivery runs on a detached goroutine and this function returns immediately.
// That is load-bearing: processClaimedTask must be free to return so its
// WaitGroup slot is released and processAvailable's wg.Wait() can advance to the
// next stale-recovery and queue-polling pass. A slow or stuck notification
// command therefore cannot delay the current daemon iteration even though task
// state has already been published.
//
// The delivery goroutine is deliberately not tracked by any daemon WaitGroup and
// runs on a context detached from the task/iteration lifecycle (see
// deliverTerminalNotification), so the only bound on a stuck command is the
// notification timeout. The shutdown trade-off is intentional: when the daemon
// process exits, an in-flight notification goroutine is abandoned and that single
// delivery may be lost. Retry and at-least-once delivery guarantees are out of
// scope; the product contract is best-effort failure visibility, not guaranteed
// delivery.
//
// Hook outcomes are logged and swallowed. Notification success or failure never
// mutates task state, never moves the task back, never retries the task, and
// never fails the daemon loop, so a broken or hanging notifier cannot hide the
// primary task outcome. The ctx parameter is intentionally ignored so a
// cancelled task/iteration context does not abort an in-flight best-effort
// delivery before its own timeout.
func notifyTerminalPublication(_ context.Context, opts Options, runningPath string, runDir *string) {
	cfg := opts.Notifications
	if cfg == nil || !cfg.Enabled || cfg.Command == "" {
		return
	}
	rd := ""
	if runDir != nil {
		rd = *runDir
	}
	base := filepath.Base(runningPath)
	if notifyDeliveryTracking != nil {
		notifyDeliveryTracking.Add(1)
	}
	go func() {
		if notifyDeliveryTracking != nil {
			defer notifyDeliveryTracking.Done()
		}
		deliverTerminalNotification(opts, base, rd)
	}()
}

// deliverTerminalNotification performs one synchronous, best-effort notification
// delivery: it reads back the published task, applies the on-filter, and runs
// the configured command bounded by the notification timeout. notifyTerminalPublication
// runs it on a detached goroutine; it is also the deterministic entry point for
// payload, filtering, and failure-swallow unit tests.
//
// The published task is read back from tasks/done|failed using the running
// task's base filename. This guarantees the hook only observes a task that
// actually reached a published terminal state: if the terminal move failed and
// the task is still under tasks/running, no published file exists and no
// notification fires. The status used for the on-filter is therefore the
// authoritative persisted status, not an in-memory guess.
//
// The context is constructed here (context.Background) rather than threaded from
// the daemon so a cancelled iteration context cannot abort delivery early; the
// only bound on a stuck command is opts.notifyTimeout (zero resolves to
// notify.DefaultTimeout). runner.RunCommand kills the process group on timeout,
// so a hanging script is terminated rather than leaking as an unmanaged
// long-running child. Hook outcomes are logged and swallowed; this function
// never mutates task state.
func deliverTerminalNotification(opts Options, base, runDir string) {
	cfg := opts.Notifications
	if cfg == nil || !cfg.Enabled || cfg.Command == "" {
		return
	}
	published, ok := findPublishedTask(opts.Root, base)
	if !ok {
		return
	}
	if !cfg.Matches(published.Status) {
		return
	}
	ev := notify.Event{
		TaskID:  published.ID,
		Status:  published.Status,
		Repo:    published.Scope.CWD,
		Summary: latestTaskSummary(published),
		RunDir:  runDir,
	}
	res, err := notify.Run(context.Background(), cfg.Command, ev, notify.Options{Timeout: opts.notifyTimeout})
	if err != nil {
		fmt.Fprintf(os.Stderr, "galley: notification hook for task %s (status=%s) failed: %v\n", published.ID, published.Status, err)
		return
	}
	fmt.Fprintf(os.Stderr, "galley: notification hook for task %s (status=%s) sent (exit=%d)\n", published.ID, published.Status, res.ExitCode)
}

// findPublishedTask loads the terminal task file matching base from the done or
// failed state directories. failed is checked first because both directories
// cannot legitimately hold the same base at once; the first match wins.
func findPublishedTask(root, base string) (task.Task, bool) {
	for _, state := range []string{"failed", "done"} {
		path := filepath.Join(root, "tasks", state, base)
		if _, err := os.Stat(path); err != nil {
			continue
		}
		loaded, err := task.Load(path)
		if err != nil {
			continue
		}
		return loaded, true
	}
	return task.Task{}, false
}

// latestTaskSummary returns the short, human-readable summary the notification
// payload carries. The most recent attempt summary is preferred; a task that
// failed before any attempt falls back to its most recent risk detail and then
// its goal so the notification is never empty for a real outcome.
func latestTaskSummary(t task.Task) string {
	if n := len(t.Attempts); n > 0 {
		if s := strings.TrimSpace(t.Attempts[n-1].Summary); s != "" {
			return s
		}
	}
	if n := len(t.Risks); n > 0 {
		if s := strings.TrimSpace(t.Risks[n-1].Detail); s != "" {
			return s
		}
	}
	return strings.TrimSpace(t.Goal)
}
