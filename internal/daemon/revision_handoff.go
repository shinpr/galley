package daemon

import (
	"fmt"
	"strings"

	"github.com/shinpr/galley/internal/supervisor"
	"github.com/shinpr/galley/internal/task"
)

const supervisorRevisionSource = "supervisor"

const supervisorGuidanceRiskPrefix = "requeue-supervisor-attempt-"

type supervisorRevision struct {
	Requests []task.RevisionRequest
}

func nextSupervisorRevision(sourceAttempt int, verdict supervisor.Verdict) supervisorRevision {
	requests := make([]task.RevisionRequest, 0, len(verdict.Findings))
	for i, finding := range verdict.Findings {
		requests = append(requests, task.RevisionRequest{
			ID:     fmt.Sprintf("supervisor-attempt-%d-finding-%d", sourceAttempt, i+1),
			Source: supervisorRevisionSource,
			Text:   strings.TrimSpace(finding),
			Status: "pending",
		})
	}
	return supervisorRevision{Requests: requests}
}

func supervisorRevisionFromTask(value task.Task) supervisorRevision {
	revision := supervisorRevision{}
	for _, request := range value.RevisionRequests {
		if request.Source == supervisorRevisionSource {
			revision.Requests = append(revision.Requests, request)
		}
	}
	return revision
}

func (revision supervisorRevision) applyToTask(value task.Task) task.Task {
	requests := make([]task.RevisionRequest, 0, len(value.RevisionRequests)+len(revision.Requests))
	for _, request := range value.RevisionRequests {
		if request.Source != supervisorRevisionSource {
			requests = append(requests, request)
		}
	}
	for _, request := range revision.Requests {
		if !task.ContainsRevisionRequest(requests, request.ID) {
			requests = append(requests, request)
		}
	}
	value.RevisionRequests = requests

	risks := make([]task.Risk, 0, len(value.Risks))
	for _, risk := range value.Risks {
		if !strings.HasPrefix(risk.ID, supervisorGuidanceRiskPrefix) {
			risks = append(risks, risk)
		}
	}
	value.Risks = risks
	return value
}
