package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/shinpr/galley/internal/retry"
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
	for _, state := range []task.WorkflowState{task.WorkflowStateDone, task.WorkflowStateFailed} {
		matches, err := task.YAMLFiles(task.TaskStateDir(root, state))
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
		fmt.Fprintf(os.Stderr, "galley: skipping PR comment scan for unreadable task %s: %v\n", path, err)
		return nil
	}
	// Gate the task on the persisted fields before resolving the repository
	// profile or calling GitHub. A closed, merged, archived, PR-less, or
	// non-open PR task cannot drive a Galley follow-up run from a /galley
	// comment, so scanning it would only add polling latency and GitHub API
	// load without changing observable behavior. Performing the eligibility
	// check here (rather than after profile load or after FetchPRComments)
	// also lets closed or archived task files survive without a usable
	// repository profile.
	if !isActionableForPRCommentPoll(loaded) {
		return nil
	}
	_, profiles, err := loadTaskProfiles(opts, loaded.Scope.CWD)
	if err != nil {
		return err
	}
	effectiveOpts := effectiveOptionsForProfiles(opts, profiles)
	if !effectiveOpts.PollPRComments {
		return nil
	}
	// Retry the PR comment listing. `gh api .../comments` is a GET and
	// idempotent, so absorbing a brief GitHub read flake here avoids losing a
	// polling cycle. The PostPRComment call sites below are intentionally NOT
	// wrapped in retry.Do — POSTing a comment is non-idempotent and has no
	// idempotency key, so retrying could create duplicate PR comments.
	var comments []vcs.PRComment
	err = retry.Do(ctx, func(ctx context.Context) error {
		fetched, fetchErr := vcs.FetchPRComments(ctx, vcsBinaries(effectiveOpts), effectiveOpts.Root, loaded.PR.URL)
		if fetchErr != nil {
			return fetchErr
		}
		comments = fetched
		return nil
	})
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
			appendRiskWithID(&loaded, "pr-requeue-"+command.CommentID, "external_dependency", fmt.Sprintf("PR comment %s requested a rerun, but Galley could not requeue the task: %v", command.CommentID, err), "Resolve the queued/running task conflict or filesystem error, then run galley task requeue manually.", true)
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

// isActionableForPRCommentPoll reports whether the persisted task fields show
// that the task could accept another /galley follow-up from a PR comment. The
// gate intentionally consults only the persisted task YAML so the daemon can
// skip non-actionable task files before resolving the repository profile or
// calling GitHub. The conditions are:
//
//  1. The task records a PR URL. PR-less tasks have no comment thread to scan.
//  2. The PR status is "open" (case-insensitive). Merged or closed PRs cannot
//     usefully trigger another Galley implementation pass.
//  3. The task status is one of "pr_opened" (the primary actionable state used
//     by the requeue path in tasks/done) or "needs_supervisor_review" (the
//     actionable failed-review state used in tasks/failed). The production
//     poller scans only tasks/done and tasks/failed, so "queued" and "running"
//     are not reached through that path; they remain allowed only so direct
//     calls to processTaskPRComments preserve the existing "already
//     queued/running" reply behavior. Other statuses such as "merged",
//     "closed", "accepted", "failed", "archived", or "draft" are
//     non-actionable and are skipped here.
//
// Keeping this check ahead of loadTaskProfiles and vcs.FetchPRComments is the
// reason the daemon does not pay per-task latency or GitHub quota for
// non-actionable tasks during PR comment polling.
func isActionableForPRCommentPoll(loaded task.Task) bool {
	if loaded.PR.URL == "" {
		return false
	}
	if !strings.EqualFold(loaded.PR.Status, "open") {
		return false
	}
	switch loaded.Status {
	case "pr_opened", "needs_supervisor_review", "queued", "running":
		return true
	default:
		return false
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
// inspected. A leading "/galley" marks the rest of the comment as the
// free-form revision request.
//
// Once a prefix matches, the rest of the body is split into the command line
// (the remainder of the first line after the prefix) and the trailing block
// (every line after the first newline). Each part is TrimSpace'd separately
// and, when both are non-empty, joined with a blank line ("\n\n"). When only
// one part is non-empty, that part becomes the Reason verbatim. An empty
// Reason falls back to a default acknowledgement string. Bodies whose first
// non-whitespace characters do not match "/galley" as a whole token are
// rejected, so mid-line mentions, /galley appearing only on a non-leading
// line, "/galley:galley ...", and "/galleyfoo ..." all fail the prefix check
// naturally without per-line scanning or code-block detection.
func parsePRCommand(comment vcs.PRComment) (prCommand, bool) {
	const (
		prefix        = "/galley"
		defaultReason = "PR comment requested another Galley run."
	)

	body := strings.TrimSpace(comment.Body)
	if body == "" {
		return prCommand{}, false
	}

	var rest string
	switch {
	case body == prefix:
	case strings.HasPrefix(body, prefix+" ") || strings.HasPrefix(body, prefix+"\n"):
		rest = body[len(prefix):]
	default:
		return prCommand{}, false
	}

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
