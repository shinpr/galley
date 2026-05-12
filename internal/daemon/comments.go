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
		// PR comment commands run local executor work, so Galley restricts
		// them to the PR author recorded for this task. When the recorded PR
		// author is empty (unknown), fail closed and reject the command.
		// Replies are concise and do not echo the user-supplied request body.
		if !prCommentMatchesPRAuthor(comment, loaded.PR.AuthorLogin) {
			loaded.PR.ProcessedCommentIDs = append(loaded.PR.ProcessedCommentIDs, command.CommentID)
			if err := task.Save(path, loaded); err != nil {
				return err
			}
			if effectiveOpts.ReplyPRComments {
				if err := vcs.PostPRComment(ctx, vcsBinaries(effectiveOpts), effectiveOpts.Root, loaded.PR.URL, "Galley ignored this comment because only the pull request author can run Galley from PR comments."); err != nil {
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
				if err := vcs.PostPRComment(ctx, vcsBinaries(effectiveOpts), effectiveOpts.Root, loaded.PR.URL, fmt.Sprintf("Galley noted this comment; task is already %s.", loaded.Status)); err != nil {
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
			if err := vcs.PostPRComment(ctx, vcsBinaries(effectiveOpts), effectiveOpts.Root, loaded.PR.URL, fmt.Sprintf("Galley requeued task `%s` from this comment.", loaded.ID)); err != nil {
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

// prCommentMatchesPRAuthor reports whether the comment author matches the PR
// author login recorded for the task. An empty prAuthorLogin means the PR
// author is unknown for this task; Galley fails closed in that case so an
// older task file without the persisted author cannot drive a Galley run
// from PR comments. The comparison is case-insensitive because GitHub login
// matching is case-insensitive.
func prCommentMatchesPRAuthor(comment vcs.PRComment, prAuthorLogin string) bool {
	if prAuthorLogin == "" {
		return false
	}
	if comment.User.Login == "" {
		return false
	}
	return strings.EqualFold(comment.User.Login, prAuthorLogin)
}

// parsePRCommand recognises Galley PR-comment requests. The full comment body
// is trimmed of surrounding whitespace and only the leading characters are
// inspected. Prefixes are evaluated in this fixed order so backward-compatible
// alias Reasons are preserved:
//
//  1. "/galley rerun"   (exact body or "/galley rerun "/"\n" prefix)
//  2. "/galley requeue" (exact body or "/galley requeue "/"\n" prefix)
//  3. "/galley "        (8-character free-form prefix: slash, galley, space)
//  4. "/galley"         (exact body, optionally followed by a newline block)
//
// Once a prefix matches, the rest of the body is split into the command line
// (the remainder of the first line after the prefix) and the trailing block
// (every line after the first newline). Each part is TrimSpace'd separately
// and, when both are non-empty, joined with a blank line ("\n\n"). When only
// one part is non-empty, that part becomes the Reason verbatim. An empty
// Reason falls back to a default acknowledgement string. Bodies whose first
// non-whitespace characters do not match one of the four prefixes are
// rejected, so mid-line mentions ("Looks good, /galley rerun"), /galley
// appearing only on a non-leading line, "/galley:galley ...", and
// "/galleyfoo ..." all fail the prefix check naturally without per-line
// scanning or code-block detection.
func parsePRCommand(comment vcs.PRComment) (prCommand, bool) {
	const (
		rerunPrefix   = "/galley rerun"
		requeuePrefix = "/galley requeue"
		freePrefix    = "/galley "
		barePrefix    = "/galley"
		defaultReason = "PR comment requested another Galley run."
	)

	body := strings.TrimSpace(comment.Body)
	if body == "" {
		return prCommand{}, false
	}

	// matchPrefix reports whether body begins with prefix as a whole token,
	// i.e. body == prefix or body starts with prefix immediately followed by a
	// space or newline. It returns the rest of the body after the prefix when
	// matched. This guards against "/galleyfoo" or "/galley:galley" matching
	// "/galley".
	matchPrefix := func(prefix string) (string, bool) {
		if body == prefix {
			return "", true
		}
		if strings.HasPrefix(body, prefix+" ") || strings.HasPrefix(body, prefix+"\n") {
			return body[len(prefix):], true
		}
		return "", false
	}

	var (
		rest    string
		matched bool
	)
	if r, ok := matchPrefix(rerunPrefix); ok {
		rest, matched = r, true
	} else if r, ok := matchPrefix(requeuePrefix); ok {
		rest, matched = r, true
	} else if strings.HasPrefix(body, freePrefix) {
		rest, matched = body[len(freePrefix):], true
	} else if r, ok := matchPrefix(barePrefix); ok {
		rest, matched = r, true
	}
	if !matched {
		return prCommand{}, false
	}

	// Preserve the historical reason-assembly semantics: split the rest of the
	// body into the command line and the trailing block, TrimSpace each part
	// separately, and join them with a blank line when both are non-empty. An
	// empty Reason falls back to the default acknowledgement string so a bare
	// "/galley" (or "/galley rerun" with no body) still produces a stable
	// Reason.
	firstLine, trailing, _ := strings.Cut(rest, "\n")
	firstLine = strings.TrimSpace(firstLine)
	trailing = strings.TrimSpace(trailing)
	var reason string
	switch {
	case firstLine != "" && trailing != "":
		reason = firstLine + "\n\n" + trailing
	case firstLine != "":
		reason = firstLine
	case trailing != "":
		reason = trailing
	default:
		reason = defaultReason
	}
	return prCommand{CommentID: strconv.FormatInt(comment.ID, 10), Reason: reason}, true
}
