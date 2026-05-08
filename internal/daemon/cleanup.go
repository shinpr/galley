package daemon

import (
	"context"
	"errors"
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
	if loaded.PR.URL == "" || loaded.Worktree.Path == "" || !loaded.Worktree.Enabled {
		return nil
	}
	state, err := vcs.FetchPRState(ctx, opts.Root, loaded.PR.URL)
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
	cleanupResult, err := workspace.Remove(ctx, loaded.Scope.CWD, loaded.Worktree)
	if err != nil {
		if errors.Is(err, workspace.ErrDirtyWorktree) {
			loaded.PR.Status = finalStatus
			if !hasCleanupRisk(loaded.Risks) {
				loaded.Risks = append(loaded.Risks, task.Risk{
					ID:                   fmt.Sprintf("cleanup-%d", len(loaded.Risks)+1),
					Type:                 "technical_debt",
					Detail:               fmt.Sprintf("%s; status=%q", err.Error(), cleanupResult.StatusPorcelain),
					Mitigation:           "Worktree cleanup skipped because local changes remain.",
					HumanReviewSuggested: true,
				})
			}
			return task.Save(path, loaded)
		}
		return err
	}
	if cleanupResult.AlreadyMissing && alreadyFinal {
		return nil
	}
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

func hasCleanupRisk(risks []task.Risk) bool {
	for _, risk := range risks {
		if strings.HasPrefix(risk.ID, "cleanup-") {
			return true
		}
	}
	return false
}
