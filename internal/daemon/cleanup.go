package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shinpr/galley/internal/retry"
	"github.com/shinpr/galley/internal/task"
	"github.com/shinpr/galley/internal/vcs"
	"github.com/shinpr/galley/internal/workspace"
)

func cleanupWorktrees(ctx context.Context, opts Options) error {
	matches, err := filepath.Glob(filepath.Join(opts.Root, "tasks", "done", "*.yaml"))
	if err != nil {
		return err
	}
	var firstErr error
	for _, path := range matches {
		if err := cleanupTaskWorktree(ctx, opts, path); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func cleanupTaskWorktree(ctx context.Context, opts Options, path string) error {
	loaded, err := task.Load(path)
	if err != nil {
		// Worktree cleanup updates task.Status/PR.Status via task.Save.
		// Re-marshalling a strict-decode-incompatible task would strip fields
		// the current schema does not know about, so skip it and keep the
		// cleanup loop moving.
		fmt.Fprintf(os.Stderr, "galley: skipping worktree cleanup for unreadable task %s: %v\n", path, err)
		return nil
	}
	_, profiles, err := loadTaskProfiles(opts, loaded.Scope.CWD)
	if err != nil {
		return cleanupTaskError(path, loaded, err)
	}
	effectiveOpts := effectiveOptionsForProfiles(opts, profiles)
	if !effectiveOpts.CleanupWorktrees {
		return nil
	}
	if loaded.PR.URL == "" || loaded.Worktree.Path == "" || !loaded.Worktree.Enabled {
		return nil
	}

	// Decide the final PR status that authorizes worktree removal.
	//
	// A persisted final PR status (merged/closed) is sufficient on its own:
	// the PR lifecycle has no remaining decision, so cleanup of an already-
	// final historical task must not depend on GitHub API availability (D1).
	// Querying GitHub for these tasks is the recurring maintenance failure
	// this change removes, so the no-GitHub path is gated strictly on the
	// persisted final status (R1).
	persistedFinal := loaded.PR.Status == "merged" || loaded.PR.Status == "closed"
	finalStatus := loaded.PR.Status
	if !persistedFinal {
		// The task still records a non-final PR (e.g. open), so the live PR
		// state may have changed after Galley last persisted the task. Refresh
		// it from GitHub. `gh pr view` is a GET-equivalent and idempotent, so a
		// brief GitHub read flake should not abort worktree cleanup.
		var state vcs.PullRequestState
		err = retry.Do(ctx, func(ctx context.Context) error {
			s, fetchErr := vcs.FetchPRState(ctx, vcsBinaries(effectiveOpts), effectiveOpts.Root, loaded.PR.URL)
			if fetchErr != nil {
				return fetchErr
			}
			state = s
			return nil
		})
		if err != nil {
			return cleanupTaskError(path, loaded, err)
		}
		if strings.EqualFold(state.State, "open") {
			return nil
		}
		if !strings.EqualFold(state.State, "closed") {
			return cleanupTaskError(path, loaded, fmt.Errorf("unsupported PR state %q", state.State))
		}
		finalStatus = closedPRTaskStatus(state)
	}

	cleanupResult, err := workspace.Remove(ctx, loaded.Scope.CWD, loaded.Worktree, workspaceOptions(effectiveOpts))
	if err != nil {
		return cleanupTaskError(path, loaded, err)
	}
	if cleanupResult.AlreadyMissing && persistedFinal {
		loaded.Status = finalStatus
		if err := task.Save(path, loaded); err != nil {
			return cleanupTaskError(path, loaded, err)
		}
		return nil
	}
	loaded.Status = finalStatus
	loaded.PR.Status = finalStatus
	now := time.Now().UTC().Format(time.RFC3339Nano)
	loaded.Attempts = append(loaded.Attempts, task.Attempt{
		Number:            len(loaded.Attempts) + 1,
		StartedAt:         now,
		CompletedAt:       now,
		ClaudeStatus:      "not_run",
		SupervisorVerdict: "cleanup",
		Summary:           fmt.Sprintf("Worktree cleanup removed=%t missing=%t path=%s", cleanupResult.Removed, cleanupResult.AlreadyMissing, cleanupResult.Path),
	})
	if err := task.Save(path, loaded); err != nil {
		return cleanupTaskError(path, loaded, err)
	}
	return nil
}

// cleanupTaskError wraps a per-task worktree cleanup failure with enough
// context for an operator or agent to identify the failing task from daemon
// logs (R2/AC5): the task YAML path, the task id, the PR URL, and the managed
// worktree path. cleanupWorktrees scans every done task and returns the first
// such contextualized failure (AC6), so the context must name which task in
// the sweep failed rather than only the underlying git or GitHub error.
func cleanupTaskError(path string, loaded task.Task, err error) error {
	return fmt.Errorf(
		"worktree cleanup failed for task %s (id=%s pr=%s worktree=%s): %w",
		path, loaded.ID, loaded.PR.URL, loaded.Worktree.Path, err,
	)
}

func closedPRTaskStatus(state vcs.PullRequestState) string {
	if state.Merged {
		return "merged"
	}
	return "closed"
}
