package daemon

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/shinpr/galley/internal/retry"
	"github.com/shinpr/galley/internal/task"
	"github.com/shinpr/galley/internal/vcs"
	"github.com/shinpr/galley/internal/workspace"
)

func cleanupWorktrees(ctx context.Context, opts Options) error {
	matches, err := task.YAMLFiles(task.TaskStateDir(opts.Root, task.WorkflowStateDone))
	if err != nil {
		return err
	}
	var firstErr error
	for _, path := range matches {
		if err := cleanupTaskWorktree(ctx, opts, path); err != nil {
			if firstErr == nil {
				firstErr = err
				continue
			}
			fmt.Fprintf(os.Stderr, "galley: additional worktree cleanup failure: %v\n", err)
		}
	}
	return firstErr
}

func cleanupTaskWorktree(ctx context.Context, opts Options, path string) error {
	loaded, err := task.Load(path)
	if err != nil {
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
	// Querying GitHub for these tasks is a recurring maintenance failure, so
	// the no-GitHub path is gated strictly on the persisted final status (R1).
	persistedFinal := loaded.PR.Status == "merged" || loaded.PR.Status == "closed"
	finalStatus, stillOpen, err := resolveFinalStatus(ctx, effectiveOpts, loaded, persistedFinal)
	if err != nil {
		return cleanupTaskError(path, loaded, err)
	}
	if stillOpen {
		return nil
	}

	cleanupResult, err := workspace.Remove(ctx, loaded.Scope.CWD, loaded.Worktree, workspaceOptions(effectiveOpts))
	if err != nil {
		return cleanupTaskError(path, loaded, err)
	}
	// Nothing is left to record: only the status is reconciled, and an
	// unchanged status leaves the task file untouched.
	if cleanupResult.AlreadyMissing && persistedFinal {
		return reconcileFinalStatus(path, loaded, finalStatus)
	}

	loaded.Status = finalStatus
	// PR.Status carries the PR lifecycle vocabulary; a final task status maps
	// onto its merged/closed values.
	loaded.PR.Status = string(finalStatus)
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

func closedPRTaskStatus(state vcs.PullRequestState) task.Status {
	if state.Merged {
		return task.StatusMerged
	}
	return task.StatusClosed
}

// refreshFinalPRStatus resolves a non-final PR's task status, reporting
// stillOpen while the PR is open. The idempotent GET is retried on a flake.
func refreshFinalPRStatus(ctx context.Context, opts Options, prURL string) (status task.Status, stillOpen bool, err error) {
	var state vcs.PullRequestState
	if err := retry.Do(ctx, func(ctx context.Context) error {
		s, fetchErr := vcs.FetchPRState(ctx, vcsRepo(opts, opts.Root, ""), prURL)
		if fetchErr != nil {
			return fetchErr
		}
		state = s
		return nil
	}); err != nil {
		return "", false, err
	}
	if strings.EqualFold(state.State, "open") {
		return "", true, nil
	}
	if !strings.EqualFold(state.State, "closed") {
		return "", false, fmt.Errorf("unsupported PR state %q", state.State)
	}
	return closedPRTaskStatus(state), false, nil
}

// resolveFinalStatus returns a completed PR's task status. An already-final
// persisted status needs no GitHub call, so cleanup survives an API outage.
func resolveFinalStatus(ctx context.Context, opts Options, loaded task.Task, persistedFinal bool) (status task.Status, stillOpen bool, err error) {
	if persistedFinal {
		return task.Status(loaded.PR.Status), false, nil
	}
	return refreshFinalPRStatus(ctx, opts, loaded.PR.URL)
}

// reconcileFinalStatus records a resolved final status on a task whose worktree
// is already gone. An unchanged status must not rewrite the task file.
func reconcileFinalStatus(path string, loaded task.Task, finalStatus task.Status) error {
	if loaded.Status == finalStatus {
		return nil
	}
	loaded.Status = finalStatus
	if err := task.Save(path, loaded); err != nil {
		return cleanupTaskError(path, loaded, err)
	}
	return nil
}
