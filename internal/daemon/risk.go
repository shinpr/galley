package daemon

import (
	"fmt"

	"github.com/shinpr/galley/internal/task"
)

func appendRisk(loaded *task.Task, prefix, riskType, detail, mitigation string, humanReview bool) {
	if loaded == nil {
		return
	}
	appendRiskWithID(loaded, fmt.Sprintf("%s-%d", prefix, len(loaded.Risks)+1), riskType, detail, mitigation, humanReview)
}

func appendRiskWithID(loaded *task.Task, id, riskType, detail, mitigation string, humanReview bool) {
	if loaded == nil {
		return
	}
	loaded.Risks = append(loaded.Risks, task.Risk{
		ID:                   id,
		Type:                 riskType,
		Detail:               detail,
		Mitigation:           mitigation,
		HumanReviewSuggested: humanReview,
	})
}
