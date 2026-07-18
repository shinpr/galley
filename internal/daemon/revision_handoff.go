package daemon

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/shinpr/galley/internal/supervisor"
	"github.com/shinpr/galley/internal/task"
)

const supervisorRevisionSource = "supervisor"

const (
	supervisorGuidanceRiskPrefix = "requeue-supervisor-attempt-"
	supervisorGuidanceRiskSuffix = "-guidance"
)

type supervisorRevision struct {
	SourceAttempt int
	Guidance      string
	Requests      []task.RevisionRequest
}

func nextSupervisorRevision(previous supervisorRevision, sourceAttempt int, verdict supervisor.Verdict) supervisorRevision {
	requests := make([]task.RevisionRequest, 0, len(previous.Requests)+len(verdict.Findings))
	knownText := make(map[string]bool)
	for _, request := range previous.Requests {
		if request.Status == "addressed" {
			requests = append(requests, request)
			continue
		}
		requests = append(requests, request)
		knownText[request.Text] = true
	}

	for i, finding := range verdict.Findings {
		request := task.RevisionRequest{
			ID:     fmt.Sprintf("supervisor-attempt-%d-finding-%d", sourceAttempt, i+1),
			Source: supervisorRevisionSource,
			Text:   supervisorFindingText(finding),
			Status: "pending",
		}
		if !knownText[request.Text] {
			requests = append(requests, request)
			knownText[request.Text] = true
		}
	}

	return supervisorRevision{SourceAttempt: sourceAttempt, Guidance: verdict.NextWorkOrder, Requests: requests}
}

func supervisorRevisionFromTask(value task.Task) supervisorRevision {
	revision := supervisorRevision{}
	for _, request := range value.RevisionRequests {
		if request.Source == supervisorRevisionSource {
			revision.Requests = append(revision.Requests, request)
		}
	}
	for _, risk := range value.Risks {
		if attempt, ok := supervisorGuidanceAttempt(risk.ID); ok {
			revision.SourceAttempt = attempt
			revision.Guidance = risk.Detail
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

	risks := make([]task.Risk, 0, len(value.Risks)+1)
	for _, risk := range value.Risks {
		if _, ok := supervisorGuidanceAttempt(risk.ID); !ok {
			risks = append(risks, risk)
		}
	}
	if strings.TrimSpace(revision.Guidance) != "" {
		risks = append(risks, task.Risk{
			ID:                   fmt.Sprintf("%s%d%s", supervisorGuidanceRiskPrefix, revision.SourceAttempt, supervisorGuidanceRiskSuffix),
			Type:                 "partial_verification",
			Detail:               revision.Guidance,
			Mitigation:           "Complete the pending supervisor revision requests before acceptance.",
			HumanReviewSuggested: false,
		})
	}
	value.Risks = risks
	return value
}

func clearSupervisorGuidance(value *task.Task) {
	risks := value.Risks[:0]
	for _, risk := range value.Risks {
		if _, ok := supervisorGuidanceAttempt(risk.ID); !ok {
			risks = append(risks, risk)
		}
	}
	value.Risks = risks
}

func supervisorGuidanceAttempt(id string) (int, bool) {
	if !strings.HasPrefix(id, supervisorGuidanceRiskPrefix) || !strings.HasSuffix(id, supervisorGuidanceRiskSuffix) {
		return 0, false
	}
	value := strings.TrimSuffix(strings.TrimPrefix(id, supervisorGuidanceRiskPrefix), supervisorGuidanceRiskSuffix)
	attempt, err := strconv.Atoi(value)
	return attempt, err == nil
}

func supervisorFindingText(finding supervisor.Finding) string {
	location := "category=" + finding.Category
	if finding.File != "" {
		location += " file=" + finding.File
	}
	return location + ": " + strings.TrimSpace(finding.Summary)
}
