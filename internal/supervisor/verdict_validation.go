package supervisor

import (
	"fmt"
	"strings"

	"github.com/shinpr/galley/internal/profile"
	"github.com/shinpr/galley/internal/task"
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
	if verdict.Status == "needs_revision" && len(verdict.Findings) == 0 && strings.TrimSpace(verdict.NextWorkOrder) == "" {
		return fmt.Errorf("needs_revision verdict requires a finding or next_work_order")
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
		if finding.Supersedes == nil {
			return fmt.Errorf("supervisor verdict findings[%d].supersedes is required", i)
		}
		for j, id := range finding.Supersedes {
			if !strings.HasPrefix(id, "revision:") || strings.TrimSpace(strings.TrimPrefix(id, "revision:")) == "" {
				return fmt.Errorf("supervisor verdict findings[%d].supersedes[%d] must be a revision:<id> string", i, j)
			}
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
	reviewTask := cloneTaskReviewState(evidence.Task)
	ReconcileReviewProgressWithContext(&reviewTask, evidence.Profiles, evidence.ReviewContractContext)
	// Non-accepted verdicts are handoffs, not claims that review is complete.
	// Preserve their actionable findings and work order when optional review
	// results are incomplete; omitted items remain open for the next attempt.
	if verdict.Status != "accepted" {
		return nil
	}
	if evidence.Profiles.Quality != nil && len(evidence.Profiles.Quality.ReviewDimensions) > 0 && (verdict.QualityPasses == nil || verdict.QualityGaps == nil) {
		return fmt.Errorf("accepted supervisor verdict requires explicit quality_passes and quality_gaps outcomes")
	}
	if err := validateQualityResults(verdict.QualityPasses, verdict.QualityGaps, evidence.Profiles.Quality, openQualityIDs(reviewTask, evidence.Profiles.Quality)); err != nil {
		return fmt.Errorf("supervisor verdict has invalid quality results: %w", err)
	}
	if err := validateAcceptanceResults(verdict, reviewTask); err != nil {
		return fmt.Errorf("supervisor verdict has invalid acceptance results: %w", err)
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
	ApplyReviewProgressWithContext(&reviewTask, evidence.Profiles, evidence.ReviewContractContext, verdict)
	if missing := openAcceptanceIDs(reviewTask); len(missing) > 0 {
		return fmt.Errorf("accepted supervisor verdict leaves acceptance items open: %s", strings.Join(missing, ", "))
	}
	if err := validateAcceptedQualityProgress(reviewTask, evidence.Profiles.Quality); err != nil {
		return err
	}
	return nil
}

func validateQualityResults(passes, gaps []string, quality *profile.Quality, required []string) error {
	configured := stringSet(qualityIDs(quality))
	seen := make(map[string]string, len(passes)+len(gaps))
	for i, rawID := range passes {
		id := strings.TrimSpace(rawID)
		if !configured[id] {
			return fmt.Errorf("quality_passes[%d] %q is not configured", i, rawID)
		}
		if previous := seen[id]; previous != "" {
			return fmt.Errorf("quality item %q appears in both or multiple results (%s, quality_passes)", id, previous)
		}
		seen[id] = "quality_passes"
	}
	for i, rawID := range gaps {
		id := strings.TrimSpace(rawID)
		if !configured[id] {
			return fmt.Errorf("quality_gaps[%d] %q is not configured", i, rawID)
		}
		if previous := seen[id]; previous != "" {
			return fmt.Errorf("quality item %q appears in both or multiple results (%s, quality_gaps)", id, previous)
		}
		seen[id] = "quality_gaps"
	}
	for _, id := range required {
		if seen[id] == "" {
			return fmt.Errorf("quality item %q has no pass or gap result", id)
		}
	}
	return nil
}

func validateAcceptedQualityProgress(reviewTask task.Task, quality *profile.Quality) error {
	if quality == nil {
		return nil
	}
	passed := map[string]bool{}
	if reviewTask.ReviewProgress != nil {
		for _, id := range reviewTask.ReviewProgress.Quality {
			passed[id] = true
		}
	}
	if quality.PassPolicy.RequiredDimensionsMustPass {
		for _, dimension := range quality.ReviewDimensions {
			if dimension.Required && !passed[dimension.ID] {
				return fmt.Errorf("accepted supervisor verdict failed required quality criterion %q", dimension.ID)
			}
		}
	}
	if score := qualityPassScore(passed, quality); score < quality.PassPolicy.MinScore {
		return fmt.Errorf("accepted supervisor verdict quality score %d is below required minimum %d", score, quality.PassPolicy.MinScore)
	}
	return nil
}

func validateAcceptanceResults(verdict Verdict, reviewTask task.Task) error {
	seen, err := validateAcceptanceResultEntries(verdict, reviewTask)
	if err != nil {
		return err
	}
	for _, id := range openAcceptanceIDs(reviewTask) {
		if seen[id] == "" {
			return fmt.Errorf("missing result for open acceptance item %q", id)
		}
	}
	return nil
}

func validateAcceptanceResultEntries(verdict Verdict, reviewTask task.Task) (map[string]string, error) {
	known := acceptanceResultIDs(reviewTask)
	seen := make(map[string]string, len(verdict.AcceptanceEvidence)+len(verdict.AcceptanceGaps))
	for i, item := range verdict.AcceptanceEvidence {
		id := strings.TrimSpace(item.ACID)
		if !known[id] {
			return nil, fmt.Errorf("acceptance_evidence[%d].ac_id %q is not an open or configured acceptance item", i, item.ACID)
		}
		if !nonEmptyStrings(item.Evidence) {
			return nil, fmt.Errorf("acceptance_evidence[%d].evidence requires non-empty values", i)
		}
		if previous := seen[id]; previous != "" {
			return nil, fmt.Errorf("acceptance item %q appears in both or multiple results (%s, acceptance_evidence)", id, previous)
		}
		seen[id] = "acceptance_evidence"
	}
	for i, rawID := range verdict.AcceptanceGaps {
		id := strings.TrimSpace(rawID)
		if !known[id] {
			return nil, fmt.Errorf("acceptance_gaps[%d] %q is not an open or configured acceptance item ID", i, rawID)
		}
		if previous := seen[id]; previous != "" {
			return nil, fmt.Errorf("acceptance item %q appears in both or multiple results (%s, acceptance_gaps)", id, previous)
		}
		seen[id] = "acceptance_gaps"
	}
	return seen, nil
}

func cloneTaskReviewState(value task.Task) task.Task {
	value.AcceptanceCriteria = append([]task.AcceptanceCriterion(nil), value.AcceptanceCriteria...)
	value.RevisionRequests = append([]task.RevisionRequest(nil), value.RevisionRequests...)
	if value.ReviewProgress == nil {
		return value
	}
	progress := *value.ReviewProgress
	progress.Acceptance = append([]string(nil), progress.Acceptance...)
	progress.Quality = append([]string(nil), progress.Quality...)
	value.ReviewProgress = &progress
	return value
}

func acceptanceResultIDs(value task.Task) map[string]bool {
	ids := make(map[string]bool, len(value.AcceptanceCriteria)+len(value.RevisionRequests))
	for _, criterion := range value.AcceptanceCriteria {
		ids[criterion.ID] = true
	}
	for _, request := range value.RevisionRequests {
		if request.Status != "addressed" {
			ids["revision:"+request.ID] = true
		}
	}
	return ids
}

func openAcceptanceIDs(value task.Task) []string {
	passed := map[string]bool{}
	if value.ReviewProgress != nil {
		for _, id := range value.ReviewProgress.Acceptance {
			passed[id] = true
		}
	}
	open := make([]string, 0, len(value.AcceptanceCriteria)+len(value.RevisionRequests))
	for _, criterion := range value.AcceptanceCriteria {
		if !passed[criterion.ID] {
			open = append(open, criterion.ID)
		}
	}
	for _, request := range value.RevisionRequests {
		if request.Status != "addressed" {
			open = append(open, "revision:"+request.ID)
		}
	}
	return open
}

func openQualityIDs(value task.Task, quality *profile.Quality) []string {
	passed := map[string]bool{}
	if value.ReviewProgress != nil {
		for _, id := range value.ReviewProgress.Quality {
			passed[id] = true
		}
	}
	var open []string
	for _, id := range qualityIDs(quality) {
		if !passed[id] {
			open = append(open, id)
		}
	}
	return open
}

func projectRevisionEvidence(value *task.Task, evidence []AcceptanceEvidence, gaps map[string]bool) {
	passed := make(map[string]string, len(evidence))
	for _, item := range evidence {
		if !nonEmptyStrings(item.Evidence) {
			continue
		}
		passed[strings.TrimSpace(item.ACID)] = strings.Join(item.Evidence, "; ")
	}
	for i := range value.RevisionRequests {
		id := "revision:" + value.RevisionRequests[i].ID
		if detail, ok := passed[id]; ok && !gaps[id] {
			value.RevisionRequests[i].Status = "addressed"
			value.RevisionRequests[i].Evidence = detail
		}
	}
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

func qualityPassScore(passed map[string]bool, quality *profile.Quality) int {
	total, passedWeight := 0, 0
	for _, dimension := range quality.ReviewDimensions {
		total += dimension.Weight
		if passed[dimension.ID] {
			passedWeight += dimension.Weight
		}
	}
	if total == 0 {
		return 100
	}
	return passedWeight * 100 / total
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
