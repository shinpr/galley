package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/shinpr/galley/internal/notify"
	"github.com/shinpr/galley/internal/task"
)

// notifyTerminalPublication runs the opt-in, best-effort notification command
// hook after a claimed task has finished processing. It is invoked from a defer
// in processClaimedTask, so by the time it runs every terminal publication has
// already happened through taskstate.Move / taskstate.FailMove.
//
// The published task is read back from tasks/done|failed using the running
// task's base filename. This guarantees the hook only observes a task that
// actually reached a published terminal state: if the terminal move failed and
// the task is still under tasks/running, no published file exists and no
// notification fires. The status used for the on-filter is therefore the
// authoritative persisted status, not an in-memory guess.
//
// Hook failures are logged and swallowed. This function never returns an error
// and never mutates task state, so a broken or hanging notifier cannot move the
// task back, fail the daemon loop, or hide the primary task outcome.
func notifyTerminalPublication(ctx context.Context, opts Options, runningPath string, runDir *string) {
	cfg := opts.Notifications
	if cfg == nil || !cfg.Enabled || cfg.Command == "" {
		return
	}
	published, ok := findPublishedTask(opts.Root, filepath.Base(runningPath))
	if !ok {
		return
	}
	if !cfg.Matches(published.Status) {
		return
	}
	rd := ""
	if runDir != nil {
		rd = *runDir
	}
	ev := notify.Event{
		TaskID:  published.ID,
		Status:  published.Status,
		Repo:    published.Scope.CWD,
		Summary: latestTaskSummary(published),
		RunDir:  rd,
	}
	res, err := notify.Run(ctx, cfg.Command, ev, notify.Options{})
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
