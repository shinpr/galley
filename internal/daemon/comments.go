package daemon

import (
	"context"
	"errors"
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
	var errs []error
	for _, path := range candidates {
		if err := processTaskPRComments(ctx, opts, path); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
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
	_, profiles, err := loadTaskProfiles(opts, loaded.Scope.CWD)
	if err != nil {
		return err
	}
	effectiveOpts := effectiveOptionsForProfiles(opts, profiles)
	if !effectiveOpts.PollPRComments {
		return nil
	}
	if loaded.PR.URL == "" {
		return nil
	}
	comments, err := vcs.FetchPRComments(ctx, vcsBinaries(effectiveOpts), effectiveOpts.Root, loaded.PR.URL)
	if err != nil {
		return err
	}
	var errs []error
	for _, comment := range comments {
		commentID := strconv.FormatInt(comment.ID, 10)
		if slices.Contains(loaded.PR.ProcessedCommentIDs, commentID) {
			continue
		}
		command, ok := parsePRCommand(comment)
		if !ok {
			continue
		}
		if !trustedPRCommentAuthor(comment) {
			loaded.PR.ProcessedCommentIDs = append(loaded.PR.ProcessedCommentIDs, command.CommentID)
			if err := task.Save(path, loaded); err != nil {
				return err
			}
			if effectiveOpts.ReplyPRComments {
				if err := vcs.PostPRComment(ctx, vcsBinaries(effectiveOpts), effectiveOpts.Root, loaded.PR.URL, fmt.Sprintf("Galley ignored comment %s from @%s because author_association=%s is not allowed.", command.CommentID, comment.User.Login, comment.AuthorAssociation)); err != nil {
					errs = append(errs, err)
				}
			}
			continue
		}
		if loaded.Status == "queued" || loaded.Status == "running" {
			applyPRCommandToLoadedTask(&loaded, command)
			if err := task.Save(path, loaded); err != nil {
				return err
			}
			if effectiveOpts.ReplyPRComments {
				if err := vcs.PostPRComment(ctx, vcsBinaries(effectiveOpts), effectiveOpts.Root, loaded.PR.URL, fmt.Sprintf("Galley noted comment %s; task is already %s.", command.CommentID, loaded.Status)); err != nil {
					errs = append(errs, err)
				}
			}
			continue
		}
		result, err := task.Requeue(path, task.RequeueOptions{
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
			applyPRCommandToLoadedTask(&loaded, command)
			loaded.Risks = append(loaded.Risks, task.Risk{
				ID:                   "pr-requeue-" + command.CommentID,
				Type:                 "external_dependency",
				Detail:               fmt.Sprintf("PR comment %s requested a rerun, but Galley could not requeue the task: %v", command.CommentID, err),
				Mitigation:           "Resolve the queued/running task conflict or filesystem error, then run galley task requeue manually.",
				HumanReviewSuggested: true,
			})
			if saveErr := task.Save(path, loaded); saveErr != nil {
				errs = append(errs, errors.Join(err, saveErr))
				continue
			}
			errs = append(errs, err)
			continue
		}
		path = result.To
		loaded = result.Task
		if effectiveOpts.ReplyPRComments {
			if err := vcs.PostPRComment(ctx, vcsBinaries(effectiveOpts), effectiveOpts.Root, loaded.PR.URL, fmt.Sprintf("Galley requeued task `%s` from comment %s. Reason: %s", loaded.ID, command.CommentID, command.Reason)); err != nil {
				errs = append(errs, err)
			}
		}
		break
	}
	return errors.Join(errs...)
}

func applyPRCommandToLoadedTask(loaded *task.Task, command prCommand) {
	if command.CommentID != "" && !slices.Contains(loaded.PR.ProcessedCommentIDs, command.CommentID) {
		loaded.PR.ProcessedCommentIDs = append(loaded.PR.ProcessedCommentIDs, command.CommentID)
	}
	request := task.RevisionRequest{
		ID:        "pr-comment-" + command.CommentID,
		Source:    "pr_comment",
		CommentID: command.CommentID,
		Text:      command.Reason,
		Status:    "pending",
	}
	if !task.ContainsRevisionRequest(loaded.RevisionRequests, request.ID) {
		loaded.RevisionRequests = append(loaded.RevisionRequests, request)
	}
}

func trustedPRCommentAuthor(comment vcs.PRComment) bool {
	switch comment.AuthorAssociation {
	case "OWNER", "COLLABORATOR":
		return true
	default:
		return false
	}
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
