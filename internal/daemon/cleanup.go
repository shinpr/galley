package daemon

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

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
		return err
	}
	_, profiles, err := loadTaskProfiles(opts, loaded.Scope.CWD)
	if err != nil {
		return err
	}
	effectiveOpts := effectiveOptionsForProfiles(opts, profiles)
	if !effectiveOpts.CleanupWorktrees {
		return nil
	}
	if loaded.PR.URL == "" || loaded.Worktree.Path == "" || !loaded.Worktree.Enabled {
		return nil
	}
	state, err := vcs.FetchPRState(ctx, vcsBinaries(effectiveOpts), effectiveOpts.Root, loaded.PR.URL)
	if err != nil {
		return err
	}
	if strings.EqualFold(state.State, "open") {
		return nil
	}
	if !strings.EqualFold(state.State, "closed") {
		return fmt.Errorf("unsupported PR state %q for %s", state.State, loaded.PR.URL)
	}
	finalStatus := closedPRTaskStatus(state)
	alreadyFinal := loaded.PR.Status == "merged" || loaded.PR.Status == "closed"
	cleanupResult, err := workspace.Remove(ctx, loaded.Scope.CWD, loaded.Worktree, workspaceOptions(effectiveOpts))
	if err != nil {
		return err
	}
	if cleanupResult.AlreadyMissing && alreadyFinal {
		loaded.Status = finalStatus
		return task.Save(path, loaded)
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
	return task.Save(path, loaded)
}

func closedPRTaskStatus(state vcs.PullRequestState) string {
	if state.Merged {
		return "merged"
	}
	return "closed"
}
