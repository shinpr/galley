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
	if verdict.QualityCoverage == nil {
		return fmt.Errorf("supervisor verdict quality_coverage is required")
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
	for i, item := range verdict.DiscussionItems {
		if item.Topic == "" {
			return fmt.Errorf("supervisor verdict discussion_items[%d].topic is required", i)
		}
		if item.Summary == "" {
			return fmt.Errorf("supervisor verdict discussion_items[%d].summary is required", i)
		}
	}
	return nil
}

func ValidateVerdictForEvidence(verdict Verdict, evidence Evidence) error {
	if err := ValidateVerdict(verdict); err != nil {
		return err
	}
	requireCompleteCoverage := verdict.Status == "accepted" || verdict.Status == "needs_revision"
	if err := validateQualityCoverage(verdict.QualityCoverage, evidence.Profiles.Quality, requireCompleteCoverage); err != nil {
		return fmt.Errorf("supervisor verdict has invalid quality coverage: %w", err)
	}
	if verdict.Status != "accepted" && len(verdict.DiscussionItems) > 0 {
		return fmt.Errorf("supervisor verdict discussion_items are only valid for accepted verdicts")
	}
	// blocks_acceptance is only meaningful for accepted verdicts; enforcing the
	// pass-policy mapping on needs_revision/hard_stop verdicts would reject
	// actionable revision feedback over a moot flag and stall the AFK loop.
	if verdict.Status != "accepted" {
		return nil
	}
	for i, finding := range verdict.Findings {
		shouldBlock := severityBlocksAcceptance(finding.Severity, evidence.Profiles.Quality)
		if finding.BlocksAcceptance != shouldBlock {
			return fmt.Errorf("supervisor verdict findings[%d].blocks_acceptance=%t does not match pass policy for severity %q", i, finding.BlocksAcceptance, finding.Severity)
		}
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
	if err := validateAcceptedQualityCoverage(verdict, evidence.Profiles.Quality); err != nil {
		return err
	}
	return nil
}

func validateAcceptedQualityCoverage(verdict Verdict, quality *profile.Quality) error {
	if quality == nil {
		return nil
	}
	if quality.PassPolicy.RequiredDimensionsMustPass {
		for _, dimension := range quality.ReviewDimensions {
			if dimension.Required && hasQualityFinding(verdict.Findings, dimension.ID) {
				return fmt.Errorf("accepted supervisor verdict failed required quality criterion %q", dimension.ID)
			}
		}
	}
	if score := qualityFindingScore(verdict.Findings, quality); score < quality.PassPolicy.MinScore {
		return fmt.Errorf("accepted supervisor verdict quality score %d is below required minimum %d", score, quality.PassPolicy.MinScore)
	}
	return nil
}

func validateQualityCoverage(coverage []QualityCoverage, quality *profile.Quality, requireComplete bool) error {
	dimensions := []profile.ReviewDimension(nil)
	if quality != nil {
		dimensions = quality.ReviewDimensions
	}
	if len(dimensions) == 0 {
		if len(coverage) > 0 {
			return fmt.Errorf("quality_coverage contains criteria but the quality profile has no review dimensions")
		}
		return nil
	}
	expected := make(map[string]bool, len(dimensions))
	for _, dimension := range dimensions {
		expected[dimension.ID] = true
	}
	seenCriteria := make(map[string]bool, len(dimensions))
	seenPairs := make(map[string]bool, len(coverage))
	for i, item := range coverage {
		if !expected[item.Criterion] {
			return fmt.Errorf("quality_coverage[%d].criterion %q is not configured", i, item.Criterion)
		}
		if strings.TrimSpace(item.ChangedSurface) == "" {
			return fmt.Errorf("quality_coverage[%d].changed_surface is required", i)
		}
		if !nonEmptyStrings(item.EvidenceChecked) {
			return fmt.Errorf("quality_coverage[%d].evidence_checked requires non-empty values", i)
		}
		pair := item.Criterion + "\x00" + item.ChangedSurface
		if seenPairs[pair] {
			return fmt.Errorf("quality_coverage contains duplicate criterion and changed_surface pair %q", item.Criterion+": "+item.ChangedSurface)
		}
		seenPairs[pair] = true
		seenCriteria[item.Criterion] = true
	}
	if requireComplete {
		for _, dimension := range dimensions {
			if !seenCriteria[dimension.ID] {
				return fmt.Errorf("quality_coverage missing criterion %q", dimension.ID)
			}
		}
	}
	return nil
}

func nonEmptyStrings(values []string) bool {
	if len(values) == 0 {
		return false
	}
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return false
		}
	}
	return true
}

func qualityFindingScore(findings []Finding, quality *profile.Quality) int {
	failed := make(map[string]bool, len(findings))
	for _, finding := range findings {
		failed[finding.Category] = true
	}
	total, passed := 0, 0
	for _, dimension := range quality.ReviewDimensions {
		total += dimension.Weight
		if !failed[dimension.ID] {
			passed += dimension.Weight
		}
	}
	if total == 0 {
		return 100
	}
	return passed * 100 / total
}

func hasQualityFinding(findings []Finding, criterion string) bool {
	for _, finding := range findings {
		if finding.Category == criterion {
			return true
		}
	}
	return false
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
