package daemon

import (
	"strings"
	"testing"

	"github.com/shinpr/galley/internal/supervisor"
	"github.com/shinpr/galley/internal/task"
)

func TestSupervisorRevisionReplacementPreservesHumanRequests(t *testing.T) {
	t.Parallel()
	loaded := task.Task{RevisionRequests: []task.RevisionRequest{
		{ID: "pr-comment-1", Source: "pr-comment", Text: "human request", Status: "pending"},
		{ID: "supervisor-attempt-1-finding-1", Source: "supervisor", Text: "old finding", Status: "pending"},
	}}
	revision := nextSupervisorRevision(2, supervisor.Verdict{
		Status:   "needs_revision",
		Summary:  "latest review",
		Findings: []string{"latest finding"},
	})

	updated := revision.applyToTask(loaded)
	if len(updated.RevisionRequests) != 2 {
		t.Fatalf("revision requests = %#v", updated.RevisionRequests)
	}
	if updated.RevisionRequests[0].ID != "pr-comment-1" || updated.RevisionRequests[1].ID != "supervisor-attempt-2-finding-1" || updated.RevisionRequests[1].Text != "latest finding" {
		t.Fatalf("revision requests = %#v", updated.RevisionRequests)
	}
	workOrder := task.RenderWorkOrder(updated)
	if strings.Contains(workOrder, "old finding") || !strings.Contains(workOrder, "latest finding") {
		t.Fatalf("work order contains stale supervisor context:\n%s", workOrder)
	}
}

func assertSupervisorRevisionState(t *testing.T, value task.Task, requestID, finding string) {
	t.Helper()
	for _, request := range value.RevisionRequests {
		if request.ID == requestID && strings.Contains(request.Text, finding) && request.Status == "pending" {
			return
		}
	}
	t.Fatalf("supervisor revision request missing id=%q finding=%q: %#v", requestID, finding, value.RevisionRequests)
}
