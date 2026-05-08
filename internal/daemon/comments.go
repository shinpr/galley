package daemon

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/shinpr/galley/internal/task"
	"github.com/shinpr/galley/internal/vcs"
)

type prCommand struct {
	CommentID string
	Reason    string
}

func pollPRComments(ctx context.Context, opts Options) error {
	candidates, err := tasksWithPR(opts.Root)
	if err != nil {
		return err
	}
	for _, path := range candidates {
		if err := processTaskPRComments(ctx, opts, path); err != nil {
			return err
		}
	}
	return nil
}

func tasksWithPR(root string) ([]string, error) {
	var paths []string
	for _, state := range []string{"done", "failed"} {
		matches, err := filepath.Glob(filepath.Join(root, "tasks", state, "*.yaml"))
		if err != nil {
			return nil, err
		}
		paths = append(paths, matches...)
	}
	slices.Sort(paths)
	return paths, nil
}

func processTaskPRComments(ctx context.Context, opts Options, path string) error {
	loaded, err := task.Load(path)
	if err != nil {
		return err
	}
	if loaded.PR.URL == "" {
		return nil
	}
	comments, err := vcs.FetchPRComments(ctx, opts.Root, loaded.PR.URL)
	if err != nil {
		return err
	}
	for _, comment := range comments {
		commentID := strconv.FormatInt(comment.ID, 10)
		if slices.Contains(loaded.PR.ProcessedCommentIDs, commentID) {
			continue
		}
		command, ok := parsePRCommand(comment)
		if !ok {
			continue
		}
		if loaded.Status == "queued" || loaded.Status == "running" {
			loaded.PR.ProcessedCommentIDs = append(loaded.PR.ProcessedCommentIDs, command.CommentID)
			if err := task.Save(path, loaded); err != nil {
				return err
			}
			if opts.ReplyPRComments {
				return vcs.PostPRComment(ctx, opts.Root, loaded.PR.URL, fmt.Sprintf("Galley noted comment %s; task is already %s.", command.CommentID, loaded.Status))
			}
			return nil
		}
		_, err := task.Requeue(path, task.RequeueOptions{
			Reason:              command.Reason,
			ProcessedCommentIDs: []string{command.CommentID},
			RevisionRequests: []task.RevisionRequest{{
				ID:        "pr-comment-" + command.CommentID,
				Source:    "pr_comment",
				CommentID: command.CommentID,
				Text:      command.Reason,
				Status:    "pending",
			}},
		})
		if err != nil {
			return err
		}
		if opts.ReplyPRComments {
			return vcs.PostPRComment(ctx, opts.Root, loaded.PR.URL, fmt.Sprintf("Galley requeued task `%s` from comment %s. Reason: %s", loaded.ID, command.CommentID, command.Reason))
		}
		return nil
	}
	return nil
}

func parsePRCommand(comment vcs.PRComment) (prCommand, bool) {
	lines := strings.Split(comment.Body, "\n")
	for i, line := range lines {
		line = strings.TrimSpace(line)
		for _, prefix := range []string{"/galley rerun", "/galley requeue"} {
			if line == prefix || strings.HasPrefix(line, prefix+" ") {
				reason := strings.TrimSpace(strings.TrimPrefix(line, prefix))
				if trailing := strings.TrimSpace(strings.Join(lines[i+1:], "\n")); trailing != "" {
					if reason != "" {
						reason += "\n\n"
					}
					reason += trailing
				}
				if reason == "" {
					reason = "PR comment requested another Galley run."
				}
				return prCommand{CommentID: strconv.FormatInt(comment.ID, 10), Reason: reason}, true
			}
		}
	}
	return prCommand{}, false
}
