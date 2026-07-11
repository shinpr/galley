package task

import (
	"fmt"
	"path/filepath"

	"github.com/shinpr/galley/internal/provider"
)

const (
	StatusDraft                 = "draft"
	StatusQueued                = "queued"
	StatusRunning               = "running"
	StatusFailed                = "failed"
	StatusNeedsSupervisorReview = "needs_supervisor_review"
	StatusAccepted              = "accepted"
	StatusPROpened              = "pr_opened"
	StatusMerged                = "merged"
	StatusClosed                = "closed"
	StatusArchived              = "archived"
)

type WorkflowState string

const (
	WorkflowStateDraft    WorkflowState = "draft"
	WorkflowStateQueued   WorkflowState = "queued"
	WorkflowStateRunning  WorkflowState = "running"
	WorkflowStateDone     WorkflowState = "done"
	WorkflowStateFailed   WorkflowState = "failed"
	WorkflowStateArchived WorkflowState = "archived"
)

var allStatuses = []string{
	StatusDraft, StatusQueued, StatusRunning, StatusNeedsSupervisorReview,
	StatusAccepted, StatusPROpened, StatusFailed, StatusClosed, StatusMerged, StatusArchived,
}

var allWorkflowStates = []WorkflowState{
	WorkflowStateDraft, WorkflowStateQueued, WorkflowStateRunning,
	WorkflowStateDone, WorkflowStateFailed, WorkflowStateArchived,
}

func AllStatuses() []string {
	return append([]string(nil), allStatuses...)
}

func AllWorkflowStates() []WorkflowState {
	return append([]WorkflowState(nil), allWorkflowStates...)
}

func WorkflowStateForStatus(status string) (WorkflowState, error) {
	switch status {
	case StatusDraft:
		return WorkflowStateDraft, nil
	case StatusQueued:
		return WorkflowStateQueued, nil
	case StatusRunning:
		return WorkflowStateRunning, nil
	case StatusAccepted, StatusPROpened, StatusMerged, StatusClosed:
		return WorkflowStateDone, nil
	case StatusFailed, StatusNeedsSupervisorReview:
		return WorkflowStateFailed, nil
	case StatusArchived:
		return WorkflowStateArchived, nil
	default:
		return "", fmt.Errorf("unknown task status %q", status)
	}
}

// WorkflowStateForTransition resolves directory-ahead transitions whose file
// move intentionally precedes the persisted status update.
func WorkflowStateForTransition(from, to string) (WorkflowState, error) {
	if from != StatusQueued || to != StatusRunning {
		return "", fmt.Errorf("unsupported directory-ahead task transition %q -> %q", from, to)
	}
	return WorkflowStateForStatus(to)
}

func TaskStateDir(root string, state WorkflowState) string {
	return filepath.Join(root, "tasks", string(state))
}

func TaskStatePath(root string, state WorkflowState, name string) string {
	return filepath.Join(TaskStateDir(root, state), name)
}

func CanQueue(status string) bool {
	return status == StatusDraft
}

func CanArchive(status string) bool {
	switch status {
	case StatusAccepted, StatusPROpened, StatusFailed, StatusNeedsSupervisorReview, StatusMerged, StatusClosed:
		return true
	default:
		return false
	}
}

func IsAcceptedTerminal(status string) bool {
	switch status {
	case StatusAccepted, StatusPROpened, StatusClosed, StatusMerged:
		return true
	default:
		return false
	}
}

func ExecutorProvider(t Task) string {
	transport, ok := provider.TransportFor(t.Executor.CLI)
	if !ok {
		return "claude"
	}
	return string(transport)
}
