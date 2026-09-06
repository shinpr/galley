package task

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/shinpr/galley/internal/fileutil"
	"go.yaml.in/yaml/v3"
)

// RequeueOptions controls how a task is returned to the queued state.
type RequeueOptions struct {
	Reason              string
	Root                string
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
	if loaded.Status == StatusQueued {
		return RequeueResult{}, fmt.Errorf("task %s is already queued", loaded.ID)
	}
	ResolveFileSources(path, &loaded)
	ApplyDefaults(&loaded)
	loaded.Status = StatusQueued
	loaded.Supervisor.ReviewIterations++
	for _, commentID := range opts.ProcessedCommentIDs {
		if commentID != "" && !containsString(loaded.PR.ProcessedCommentIDs, commentID) {
			loaded.PR.ProcessedCommentIDs = append(loaded.PR.ProcessedCommentIDs, commentID)
		}
	}
	for _, request := range opts.RevisionRequests {
		if request.ID == "" {
			request.ID = nextRevisionRequestID(loaded.RevisionRequests)
		}
		if request.Status == "" {
			request.Status = "pending"
		}
		if request.Source == "" {
			request.Source = "manual"
		}
		if !ContainsRevisionRequest(loaded.RevisionRequests, request.ID) {
			loaded.RevisionRequests = append(loaded.RevisionRequests, request)
		}
	}
	appendLifecycleAttempt(&loaded, lifecycleAttempt{Verdict: "requeued", Reason: opts.Reason, Fallback: "Task requeued for another executor attempt."}, time.Now())

	nextPath := queuedPathFor(path, opts.Root)
	if err := saveOrMoveTask(path, nextPath, loaded, taskPathUnderRoot(path, opts.Root)); err != nil {
		return RequeueResult{}, err
	}
	return RequeueResult{Task: loaded, From: path, To: nextPath}, nil
}

func nextRevisionRequestID(requests []RevisionRequest) string {
	for i := len(requests) + 1; ; i++ {
		id := fmt.Sprintf("revision-%d", i)
		if !ContainsRevisionRequest(requests, id) {
			return id
		}
	}
}

// WriteMovedTask writes task YAML to dst without overwriting and removes src after success.
func WriteMovedTask(src, dst string, loaded Task) error {
	return writeQueuedTask(src, dst, loaded, true)
}

// saveOrMoveTask writes the task in place when the destination is unchanged
// and otherwise moves it without overwriting an existing file.
func saveOrMoveTask(src, dst string, loaded Task, removeSource bool) error {
	if dst == src {
		return Save(src, loaded)
	}
	return writeQueuedTask(src, dst, loaded, removeSource)
}

func writeQueuedTask(src, dst string, loaded Task, removeSource bool) error {
	data, err := yaml.Marshal(loaded)
	if err != nil {
		return fmt.Errorf("encode %s: %w", dst, err)
	}
	if err := fileutil.WriteFileNoOverwriteAtomic(dst, data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", dst, err)
	}
	if !removeSource {
		return nil
	}
	err = os.Remove(src)
	if err == nil {
		return nil
	}
	if rollbackErr := os.Remove(dst); rollbackErr != nil {
		return errors.Join(
			fmt.Errorf("remove moved task %s: %w", src, err),
			fmt.Errorf("rollback queued task %s: %w", dst, rollbackErr),
		)
	}
	return fmt.Errorf("remove moved task %s: %w", src, err)
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// ContainsRevisionRequest reports whether values already include wantID.
func ContainsRevisionRequest(values []RevisionRequest, wantID string) bool {
	for _, value := range values {
		if value.ID == wantID {
			return true
		}
	}
	return false
}
