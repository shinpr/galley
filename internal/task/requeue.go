package task

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"go.yaml.in/yaml/v3"
)

// RequeueOptions controls how a task is returned to the queued state.
type RequeueOptions struct {
	Reason              string
	ProcessedCommentIDs []string
	RevisionRequests    []RevisionRequest
}

// RequeueResult describes the file move and updated task state.
type RequeueResult struct {
	Task Task   `json:"task"`
	From string `json:"from"`
	To   string `json:"to"`
}

// Requeue updates a task YAML file to queued and moves it into tasks/queued when possible.
func Requeue(path string, opts RequeueOptions) (RequeueResult, error) {
	loaded, err := Load(path)
	if err != nil {
		return RequeueResult{}, err
	}
	if loaded.Status == "queued" {
		return RequeueResult{}, fmt.Errorf("task %s is already queued", loaded.ID)
	}
	ApplyDefaults(&loaded)
	loaded.Status = "queued"
	loaded.Supervisor.ApprovalStatus = "pending"
	loaded.Supervisor.ReviewIterations++
	for _, commentID := range opts.ProcessedCommentIDs {
		if commentID != "" && !containsString(loaded.PR.ProcessedCommentIDs, commentID) {
			loaded.PR.ProcessedCommentIDs = append(loaded.PR.ProcessedCommentIDs, commentID)
		}
	}
	for _, request := range opts.RevisionRequests {
		if request.ID == "" {
			request.ID = fmt.Sprintf("revision-%d", len(loaded.RevisionRequests)+1)
		}
		if request.Status == "" {
			request.Status = "pending"
		}
		if request.Source == "" {
			request.Source = "manual"
		}
		if !containsRevisionRequest(loaded.RevisionRequests, request.ID) {
			loaded.RevisionRequests = append(loaded.RevisionRequests, request)
		}
	}
	if opts.Reason != "" {
		loaded.Decisions = append(loaded.Decisions, Decision{
			ID:            fmt.Sprintf("requeue-%d", len(loaded.Decisions)+1),
			Question:      "Why was this task requeued?",
			Chosen:        opts.Reason,
			Rationale:     "A reviewer or PR comment requested another executor attempt.",
			Reversibility: "high",
		})
	}
	loaded.Attempts = append(loaded.Attempts, Attempt{
		Number:            len(loaded.Attempts) + 1,
		StartedAt:         time.Now().UTC().Format(time.RFC3339Nano),
		CompletedAt:       time.Now().UTC().Format(time.RFC3339Nano),
		ClaudeStatus:      "not_run",
		SupervisorVerdict: "requeued",
		Summary:           firstNonEmpty(opts.Reason, "Task requeued for another executor attempt."),
	})

	nextPath := queuedPathFor(path)
	if nextPath == path {
		if err := Save(path, loaded); err != nil {
			return RequeueResult{}, err
		}
	} else {
		if err := writeMovedTask(path, nextPath, loaded); err != nil {
			return RequeueResult{}, err
		}
	}
	return RequeueResult{Task: loaded, From: path, To: nextPath}, nil
}

func queuedPathFor(path string) string {
	return siblingTaskPath(path, "queued")
}

func writeMovedTask(src, dst string, loaded Task) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("create queue dir %s: %w", filepath.Dir(dst), err)
	}
	data, err := yaml.Marshal(loaded)
	if err != nil {
		return fmt.Errorf("encode %s: %w", dst, err)
	}
	file, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("destination already exists: %s", dst)
		}
		return fmt.Errorf("reserve destination %s: %w", dst, err)
	}
	if _, err := file.Write(data); err != nil {
		_ = os.Remove(dst)
		_ = file.Close()
		return fmt.Errorf("write %s: %w", dst, err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(dst)
		return fmt.Errorf("write %s: %w", dst, err)
	}
	if err := os.Remove(src); err != nil {
		_ = os.Remove(dst)
		return fmt.Errorf("remove moved task %s: %w", src, err)
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func containsRevisionRequest(values []RevisionRequest, wantID string) bool {
	for _, value := range values {
		if value.ID == wantID {
			return true
		}
	}
	return false
}
