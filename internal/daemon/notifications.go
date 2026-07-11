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

type notificationDispatcher struct {
	ctx context.Context
	wg  sync.WaitGroup
}

func newNotificationDispatcher(ctx context.Context) *notificationDispatcher {
	return &notificationDispatcher{ctx: ctx}
}

func (d *notificationDispatcher) Start(opts Options, runningPath string, runDir *string) {
	if d == nil {
		return
	}
	cfg := opts.Notifications
	if cfg == nil || !cfg.Enabled || cfg.Command == "" {
		return
	}
	rd := ""
	if runDir != nil {
		rd = *runDir
	}
	base := filepath.Base(runningPath)
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		deliverTerminalNotification(d.ctx, opts, base, rd)
	}()
}

func (d *notificationDispatcher) Wait() {
	if d == nil {
		return
	}
	d.wg.Wait()
}

// notifyTerminalPublication starts the opt-in, best-effort notification command
// hook for a claimed task that has finished processing. It is invoked from a
// defer in processClaimedTask, so by the time it runs every terminal
// publication has already happened through the taskstate publication APIs.
//
// Delivery runs on a dispatcher goroutine and this function returns
// immediately. That is load-bearing: processClaimedTask must be free to return
// so its WaitGroup slot is released and processAvailable's wg.Wait() can advance
// to the next stale-recovery and queue-polling pass. A slow or stuck
// notification command therefore cannot delay the current daemon iteration even
// though task state has already been published.
//
// The dispatcher is still tied to the daemon process lifecycle. Daemon shutdown
// cancels the dispatcher context and Run waits for in-flight notification
// goroutines before clearing the child registry, so a notification subprocess is
// killed through runner.RunCommand cancellation instead of being abandoned as an
// unmanaged child. Retry and at-least-once delivery guarantees are out of scope;
// the product contract is best-effort failure visibility, not guaranteed
// delivery.
//
// Hook outcomes are logged and swallowed. Notification success or failure never
// mutates task state, never moves the task back, never retries the task, and
// never fails the daemon loop, so a broken or hanging notifier cannot hide the
// primary task outcome.
func notifyTerminalPublication(_ context.Context, opts Options, runningPath string, runDir *string) {
	if opts.notifyDispatcher == nil {
		return
	}
	opts.notifyDispatcher.Start(opts, runningPath, runDir)
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
// The context is the daemon lifecycle context, not the per-task graceful
// execution context. A normal daemon shutdown cancels it so runner.RunCommand
// kills any active notification process group before Run clears the child
// registry. The notification timeout remains the wall-clock bound while the
// daemon keeps running. Hook outcomes are logged and swallowed; this function
// never mutates task state.
func deliverTerminalNotification(ctx context.Context, opts Options, base, runDir string) {
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
	res, err := notify.Run(ctx, cfg.Command, ev, notify.Options{Timeout: opts.notifyTimeout})
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
	for _, state := range []task.WorkflowState{task.WorkflowStateFailed, task.WorkflowStateDone} {
		path := task.TaskStatePath(root, state, base)
		if _, err := os.Stat(path); err != nil {
			continue
		}
		loaded, err := task.Load(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "galley: notification hook skipped unreadable published task %s: %v\n", path, err)
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
