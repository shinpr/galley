package supervisor

import (
	"fmt"
)

func ValidateVerdict(verdict Verdict) error {
	switch verdict.Status {
	case "accepted", "needs_revision", "needs_supervisor_review", "hard_stop":
	default:
		return fmt.Errorf("invalid supervisor verdict status %q", verdict.Status)
	}
	if verdict.Summary == "" {
		return fmt.Errorf("supervisor verdict summary is required")
	}
	if verdict.AcceptancePasses == nil {
		return fmt.Errorf("supervisor verdict acceptance_passes is required")
	}
	if verdict.QualityPasses == nil {
		return fmt.Errorf("supervisor verdict quality_passes is required")
	}
	if verdict.Findings == nil {
		return fmt.Errorf("supervisor verdict findings is required")
	}
	if verdict.DiscussionItems == nil {
		return fmt.Errorf("supervisor verdict discussion_items is required")
	}
	return nil
}

func ValidateVerdictForEvidence(verdict Verdict, _ Evidence) error {
	return ValidateVerdict(verdict)
}
