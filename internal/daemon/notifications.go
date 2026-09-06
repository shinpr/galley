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

func notifyTerminalPublication(_ context.Context, opts Options, runningPath string, runDir *string) {
	if opts.notifyDispatcher == nil {
		return
	}
	opts.notifyDispatcher.Start(opts, runningPath, runDir)
}

// deliverTerminalNotification notifies from published state using the daemon
// context; hook failures are logged and never mutate task state.
func deliverTerminalNotification(ctx context.Context, opts Options, base, runDir string) {
	cfg := opts.Notifications
	if cfg == nil || !cfg.Enabled || cfg.Command == "" {
		return
	}
	published, ok := findPublishedTask(opts.Root, base)
	if !ok {
		return
	}
	if !cfg.Matches(string(published.Status)) {
		return
	}
	ev := notify.Event{
		TaskID:  published.ID,
		Status:  string(published.Status),
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
