package supervisor

import (
	"fmt"
	"strings"

	"github.com/shinpr/galley/internal/profile"
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
	if verdict.Confidence == "" {
		return fmt.Errorf("supervisor verdict confidence is required")
	}
	if verdict.Status == "needs_revision" && verdict.NextWorkOrder == "" {
		return fmt.Errorf("needs_revision verdict requires next_work_order")
	}
	switch verdict.Confidence {
	case "high", "medium", "low":
	default:
		return fmt.Errorf("invalid supervisor verdict confidence %q", verdict.Confidence)
	}
	for i, finding := range verdict.Findings {
		if !validSeverity(finding.Severity) {
			return fmt.Errorf("supervisor verdict findings[%d].severity is invalid", i)
		}
		if finding.Category == "" {
			return fmt.Errorf("supervisor verdict findings[%d].category is required", i)
		}
		if finding.Summary == "" {
			return fmt.Errorf("supervisor verdict findings[%d].summary is required", i)
		}
	}
	return nil
}

func ValidateVerdictForEvidence(verdict Verdict, evidence Evidence) error {
	if err := ValidateVerdict(verdict); err != nil {
		return err
	}
	for i, finding := range verdict.Findings {
		shouldBlock := severityBlocksAcceptance(finding.Severity, evidence.Profiles.Quality)
		if finding.BlocksAcceptance != shouldBlock {
			return fmt.Errorf("supervisor verdict findings[%d].blocks_acceptance=%t does not match pass policy for severity %q", i, finding.BlocksAcceptance, finding.Severity)
		}
	}
	if verdict.Status != "accepted" {
		return nil
	}
	if evidence.DiffDirty && len(verdict.ReviewedFiles) == 0 {
		return fmt.Errorf("accepted supervisor verdict requires reviewed_files when diff is present")
	}
	if verdict.Confidence == "low" {
		return fmt.Errorf("accepted supervisor verdict cannot have low confidence")
	}
	for i, finding := range verdict.Findings {
		if finding.BlocksAcceptance {
			return fmt.Errorf("accepted supervisor verdict has blocking finding at findings[%d]: %s", i, finding.Summary)
		}
	}
	if missing := missingAcceptanceEvidence(verdict, evidence); len(missing) > 0 {
		return fmt.Errorf("accepted supervisor verdict missing acceptance evidence for %s", strings.Join(missing, ", "))
	}
	return nil
}

func missingAcceptanceEvidence(verdict Verdict, evidence Evidence) []string {
	covered := make(map[string]bool, len(verdict.AcceptanceEvidence))
	for _, item := range verdict.AcceptanceEvidence {
		if item.ACID != "" && len(item.Evidence) > 0 {
			covered[item.ACID] = true
		}
	}
	var missing []string
	for _, ac := range evidence.Task.AcceptanceCriteria {
		if !covered[ac.ID] {
			missing = append(missing, ac.ID)
		}
	}
	for _, request := range evidence.Task.RevisionRequests {
		if request.Status == "addressed" {
			continue
		}
		id := "revision:" + request.ID
		if !covered[id] {
			missing = append(missing, id)
		}
	}
	return missing
}

func severityBlocksAcceptance(severity string, quality *profile.Quality) bool {
	for _, blocking := range blockingSeverities(quality) {
		if severity == blocking {
			return true
		}
	}
	return false
}

func blockingSeverities(quality *profile.Quality) []string {
	if quality != nil && len(quality.PassPolicy.BlockingSeverities) > 0 {
		return quality.PassPolicy.BlockingSeverities
	}
	return []string{"critical", "high", "medium"}
}

func validSeverity(value string) bool {
	switch value {
	case "critical", "high", "medium", "low":
		return true
	default:
		return false
	}
}
