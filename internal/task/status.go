package task

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
	if t.Executor.CLI == "codex" {
		return "codex"
	}
	return "claude"
}
