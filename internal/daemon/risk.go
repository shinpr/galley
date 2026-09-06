package daemon

import (
	"fmt"

	"github.com/shinpr/galley/internal/task"
)

// riskSpec is one risk Galley records on the task.
type riskSpec struct {
	Type        string
	Detail      string
	Mitigation  string
	HumanReview bool
}

func appendRisk(loaded *task.Task, prefix string, risk riskSpec) {
	if loaded == nil {
		return
	}
	appendRiskWithID(loaded, fmt.Sprintf("%s-%d", prefix, len(loaded.Risks)+1), risk)
}

func appendRiskWithID(loaded *task.Task, id string, risk riskSpec) {
	if loaded == nil {
		return
	}
	loaded.Risks = append(loaded.Risks, task.Risk{
		ID:                   id,
		Type:                 risk.Type,
		Detail:               risk.Detail,
		Mitigation:           risk.Mitigation,
		HumanReviewSuggested: risk.HumanReview,
	})
}
