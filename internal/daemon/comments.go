package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/shinpr/galley/internal/runner"
	"github.com/shinpr/galley/internal/task"
)

type prComment struct {
	ID      int64  `json:"id"`
	Body    string `json:"body"`
	HTMLURL string `json:"html_url"`
}

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
	comments, err := fetchPRComments(ctx, opts.Root, loaded.PR.URL)
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
				return postPRComment(ctx, opts.Root, loaded.PR.URL, fmt.Sprintf("Galley noted comment %s; task is already %s.", command.CommentID, loaded.Status))
			}
			return nil
		}
		_, err := task.Requeue(path, task.RequeueOptions{
			Reason:              command.Reason,
			ProcessedCommentIDs: []string{command.CommentID},
		})
		if err != nil {
			return err
		}
		if opts.ReplyPRComments {
			return postPRComment(ctx, opts.Root, loaded.PR.URL, fmt.Sprintf("Galley requeued task `%s` from comment %s. Reason: %s", loaded.ID, command.CommentID, command.Reason))
		}
		return nil
	}
	return nil
}

func fetchPRComments(ctx context.Context, root, prURL string) ([]prComment, error) {
	owner, repo, number, err := parseGitHubPRURL(prURL)
	if err != nil {
		return nil, err
	}
	apiPath := fmt.Sprintf("repos/%s/%s/issues/%s/comments", owner, repo, number)
	result, err := runner.RunCommand(ctx, runner.ClaudeCommand{
		WorkDir: root,
		Argv:    []string{"gh", "api", apiPath, "--paginate", "--slurp"},
	}, runner.RunOptions{})
	if err != nil {
		return nil, fmt.Errorf("gh api PR comments failed: %w", err)
	}
	comments, err := decodePRComments(result.Stdout)
	if err != nil {
		return nil, fmt.Errorf("decode PR comments: %w", err)
	}
	return comments, nil
}

func postPRComment(ctx context.Context, root, prURL, body string) error {
	owner, repo, number, err := parseGitHubPRURL(prURL)
	if err != nil {
		return err
	}
	apiPath := fmt.Sprintf("repos/%s/%s/issues/%s/comments", owner, repo, number)
	result, err := runner.RunCommand(ctx, runner.ClaudeCommand{
		WorkDir: root,
		Argv:    []string{"gh", "api", apiPath, "-f", "body=" + body},
	}, runner.RunOptions{})
	if err != nil {
		return fmt.Errorf("gh api post PR comment failed: %w", err)
	}
	if strings.TrimSpace(result.Stdout) == "" {
		return nil
	}
	return nil
}

func decodePRComments(stdout string) ([]prComment, error) {
	var pages [][]prComment
	if err := json.Unmarshal([]byte(stdout), &pages); err == nil {
		var comments []prComment
		for _, page := range pages {
			comments = append(comments, page...)
		}
		return comments, nil
	}
	var comments []prComment
	if err := json.Unmarshal([]byte(stdout), &comments); err != nil {
		return nil, err
	}
	return comments, nil
}

func parseGitHubPRURL(raw string) (string, string, string, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", "", "", fmt.Errorf("parse PR URL: %w", err)
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) < 4 || parts[2] != "pull" {
		return "", "", "", fmt.Errorf("unsupported PR URL: %s", raw)
	}
	if parts[0] == "" || parts[1] == "" || parts[3] == "" {
		return "", "", "", fmt.Errorf("unsupported PR URL: %s", raw)
	}
	return parts[0], parts[1], parts[3], nil
}

func parsePRCommand(comment prComment) (prCommand, bool) {
	for _, line := range strings.Split(comment.Body, "\n") {
		line = strings.TrimSpace(line)
		for _, prefix := range []string{"/galley rerun", "/galley requeue"} {
			if line == prefix || strings.HasPrefix(line, prefix+" ") {
				reason := strings.TrimSpace(strings.TrimPrefix(line, prefix))
				if reason == "" {
					reason = "PR comment requested another Galley run."
				}
				return prCommand{CommentID: strconv.FormatInt(comment.ID, 10), Reason: reason}, true
			}
		}
	}
	return prCommand{}, false
}
