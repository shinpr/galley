package task

import (
	"fmt"
	"path/filepath"

	"github.com/shinpr/galley/internal/provider"
)

// Status is a Galley-owned task status, distinct from the status an executor
// reports, so the two vocabularies cannot be assigned to each other.
type Status string

const (
	StatusDraft                 Status = "draft"
	StatusQueued                Status = "queued"
	StatusRunning               Status = "running"
	StatusFailed                Status = "failed"
	StatusNeedsSupervisorReview Status = "needs_supervisor_review"
	StatusAccepted              Status = "accepted"
	StatusPROpened              Status = "pr_opened"
	StatusMerged                Status = "merged"
	StatusClosed                Status = "closed"
	StatusArchived              Status = "archived"
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

var allStatuses = []Status{
	StatusDraft, StatusQueued, StatusRunning, StatusNeedsSupervisorReview,
	StatusAccepted, StatusPROpened, StatusFailed, StatusClosed, StatusMerged, StatusArchived,
}

var allWorkflowStates = []WorkflowState{
	WorkflowStateDraft, WorkflowStateQueued, WorkflowStateRunning,
	WorkflowStateDone, WorkflowStateFailed, WorkflowStateArchived,
}

func AllStatuses() []Status {
	return append([]Status(nil), allStatuses...)
}

func AllWorkflowStates() []WorkflowState {
	return append([]WorkflowState(nil), allWorkflowStates...)
}

func WorkflowStateForStatus(status Status) (WorkflowState, error) {
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
func WorkflowStateForTransition(from, to Status) (WorkflowState, error) {
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

func CanQueue(status Status) bool {
	return status == StatusDraft
}

// CanArchive reports whether a task has settled enough to archive; draft,
// queued, and running are still live and archived is already there.
func CanArchive(status Status) bool {
	switch status {
	case StatusAccepted, StatusPROpened, StatusFailed, StatusNeedsSupervisorReview, StatusMerged, StatusClosed:
		return true
	case StatusDraft, StatusQueued, StatusRunning, StatusArchived:
		return false
	default:
		return false
	}
}

// IsAcceptedTerminal reports whether the supervisor accepted the work, so a
// prior failed attempt is history rather than the active state.
func IsAcceptedTerminal(status Status) bool {
	switch status {
	case StatusAccepted, StatusPROpened, StatusClosed, StatusMerged:
		return true
	case StatusDraft, StatusQueued, StatusRunning, StatusFailed, StatusNeedsSupervisorReview, StatusArchived:
		return false
	default:
		return false
	}
}

func ExecutorTransport(t Task) provider.Transport {
	transport, ok := provider.TransportFor(t.Executor.CLI)
	if !ok {
		return provider.TransportClaude
	}
	return transport
}
