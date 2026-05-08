package supervisor

import (
	"fmt"
	"strings"

	"github.com/shinpr/galley/internal/profile"
	"github.com/shinpr/galley/internal/runner"
	"github.com/shinpr/galley/internal/task"
)

// Verdict is the deterministic supervisor decision for one executor attempt.
type Verdict struct {
	Status             string               `json:"status"`
	Summary            string               `json:"summary"`
	AcceptanceGaps     []string             `json:"acceptance_gaps,omitempty"`
	QualityFindings    []string             `json:"quality_findings,omitempty"`
	ReviewedFiles      []string             `json:"reviewed_files,omitempty"`
	AcceptanceEvidence []AcceptanceEvidence `json:"acceptance_evidence,omitempty"`
	Findings           []Finding            `json:"findings,omitempty"`
	ResidualRisks      []string             `json:"residual_risks,omitempty"`
	Confidence         string               `json:"confidence,omitempty"`
	NextWorkOrder      string               `json:"next_work_order,omitempty"`
}

// AcceptanceEvidence links one task or revision acceptance item to concrete evidence.
type AcceptanceEvidence struct {
	ACID     string   `json:"ac_id"`
	Evidence []string `json:"evidence"`
}

// Finding is a structured supervisor review issue.
type Finding struct {
	Severity         string `json:"severity"`
	Category         string `json:"category"`
	File             string `json:"file,omitempty"`
	Summary          string `json:"summary"`
	BlocksAcceptance bool   `json:"blocks_acceptance"`
}

// Evidence is the local evidence available to the deterministic supervisor.
type Evidence struct {
	Task         task.Task
	Profiles     profile.Bundle
	Claude       runner.ClaudeResult
	ParseError   error
	RunError     error
	DiffDirty    bool
	Diff         string
	DiffError    error
	Attempt      int
	AttemptsLeft int
}

// Evaluate produces a conservative supervisor verdict from Claude output and git evidence.
func Evaluate(e Evidence) Verdict {
	if e.RunError != nil {
		return finalOrRevision(e, "executor_failed", e.RunError.Error())
	}
	if e.ParseError != nil {
		return finalOrRevision(e, "invalid_result", e.ParseError.Error())
	}
	if e.DiffError != nil {
		return finalOrRevision(e, "missing_diff_evidence", e.DiffError.Error())
	}

	switch e.Claude.Status {
	case "hard_stop":
		return hardStopVerdict(e)
	case "completed_with_risks":
		return finalOrRevision(e, "completed_with_risks", "Claude completed with risks that require supervisor review.")
	case "completed":
	default:
		return finalOrRevision(e, "invalid_status", fmt.Sprintf("Claude returned unsupported status %q.", e.Claude.Status))
	}

	var gaps []string
	reportedAC := make(map[string]runner.ClaudeAcceptanceCriterion, len(e.Claude.AcceptanceCriteria))
	for _, ac := range e.Claude.AcceptanceCriteria {
		reportedAC[ac.ID] = ac
		switch ac.Status {
		case "not_satisfied", "partially_satisfied":
			gaps = append(gaps, fmt.Sprintf("%s is %s: %s", ac.ID, ac.Status, ac.Notes))
		}
	}
	for _, required := range e.Task.AcceptanceCriteria {
		reported, ok := reportedAC[required.ID]
		if !ok {
			gaps = append(gaps, fmt.Sprintf("%s was not reported by Claude: %s", required.ID, required.Text))
			continue
		}
		if reported.Status != "satisfied" {
			continue
		}
		if len(reported.Evidence) == 0 {
			gaps = append(gaps, fmt.Sprintf("%s is satisfied but has no evidence: %s", required.ID, required.Text))
		}
	}
	for _, verification := range e.Claude.Verification {
		if verification.Status == "failed" {
			gaps = append(gaps, fmt.Sprintf("verification failed: %s (%s)", verification.Command, verification.Reason))
		}
	}
	gaps = append(gaps, qualityProfileGaps(e)...)
	gaps = append(gaps, revisionRequestGaps(e)...)
	if !e.DiffDirty {
		gaps = append(gaps, "Executor reported completion but produced no git diff in the execution workspace.")
	}
	if len(gaps) > 0 {
		return acceptanceRevision(e, gaps)
	}

	return Verdict{
		Status:             "accepted",
		Summary:            "Task accepted by deterministic supervisor checks.",
		AcceptanceEvidence: deterministicAcceptanceEvidence(e),
		Confidence:         "high",
	}
}

func hardStopVerdict(e Evidence) Verdict {
	reason := "Claude reported a hard stop."
	if e.Claude.HardStop != nil && e.Claude.HardStop.Reason != "" {
		reason = e.Claude.HardStop.Reason
	}
	findings := []string{"executor returned hard_stop: " + reason}
	if e.Claude.HardStop != nil {
		if len(e.Claude.HardStop.Attempted) > 0 {
			findings = append(findings, "attempted before hard_stop: "+strings.Join(e.Claude.HardStop.Attempted, "; "))
		}
		if len(e.Claude.HardStop.NeededToContinue) > 0 {
			findings = append(findings, "executor claimed these unblock requirements: "+strings.Join(e.Claude.HardStop.NeededToContinue, "; "))
		}
	}
	if hardStopRequiresHuman(e.Claude.HardStop) {
		return Verdict{
			Status:          "hard_stop",
			Summary:         reason,
			QualityFindings: findings,
			Findings:        findingsFromGaps(nil, findings),
			Confidence:      "high",
		}
	}
	return finalOrRevision(e, "retryable_hard_stop", findings...)
}

func hardStopRequiresHuman(stop *runner.ClaudeHardStop) bool {
	if stop == nil {
		return false
	}
	text := strings.ToLower(stop.Reason + " " + strings.Join(stop.NeededToContinue, " "))
	blockers := []string{
		"secret",
		"credential",
		"token",
		"api key",
		"paid service",
		"external service",
		"unavailable service",
		"destructive",
		"outside allowed",
		"outside write scope",
		"permission policy",
		"human approval",
		"mutually contradictory",
	}
	for _, blocker := range blockers {
		if strings.Contains(text, blocker) {
			return true
		}
	}
	return false
}

func revisionRequestGaps(e Evidence) []string {
	var gaps []string
	for _, request := range e.Task.RevisionRequests {
		if request.Status == "addressed" {
			continue
		}
		gaps = append(gaps, fmt.Sprintf("revision request %s remains pending: %s", request.ID, request.Text))
	}
	return gaps
}

func qualityProfileGaps(e Evidence) []string {
	if e.Profiles.Quality == nil {
		return nil
	}
	var gaps []string
	for _, check := range e.Profiles.Quality.RequiredChecks {
		if !check.Required {
			continue
		}
		if !requiredCheckPassed(check, e) {
			gaps = append(gaps, fmt.Sprintf("required quality check %s did not pass using preferred commands: %s", check.ID, strings.Join(check.PreferredCommands, ", ")))
		}
	}
	return gaps
}

func requiredCheckPassed(check profile.RequiredCheck, e Evidence) bool {
	for _, command := range check.PreferredCommands {
		if command == "" {
			continue
		}
		for _, verification := range e.Claude.Verification {
			if verification.Status == "passed" && sameCommand(verification.Command, command) {
				return true
			}
		}
		for _, verification := range e.Task.Verification.Commands {
			if verification.Status == "passed" && sameCommand(verification.Cmd, command) {
				return true
			}
		}
	}
	return false
}

func sameCommand(got, want string) bool {
	got = strings.TrimSpace(got)
	want = strings.TrimSpace(want)
	return got == want || strings.Contains(got, want)
}

func acceptanceRevision(e Evidence, gaps []string) Verdict {
	return buildVerdict(e, "acceptance_gaps", gaps, nil)
}

func finalOrRevision(e Evidence, summary string, findings ...string) Verdict {
	return buildVerdict(e, summary, nil, findings)
}

func buildVerdict(e Evidence, summary string, acceptanceGaps, qualityFindings []string) Verdict {
	status := "needs_revision"
	if e.AttemptsLeft <= 0 || e.RunError != nil {
		status = "needs_supervisor_review"
	}
	verdict := Verdict{
		Status:          status,
		Summary:         summary,
		AcceptanceGaps:  acceptanceGaps,
		QualityFindings: qualityFindings,
		Findings:        findingsFromGaps(acceptanceGaps, qualityFindings),
		Confidence:      "high",
	}
	if status == "needs_revision" {
		verdict.NextWorkOrder = RenderCorrectiveWorkOrder(e.Task, verdict)
	}
	return verdict
}

func deterministicAcceptanceEvidence(e Evidence) []AcceptanceEvidence {
	var evidence []AcceptanceEvidence
	for _, ac := range e.Task.AcceptanceCriteria {
		var values []string
		for _, reported := range e.Claude.AcceptanceCriteria {
			if reported.ID == ac.ID {
				values = append(values, reported.Evidence...)
			}
		}
		for _, verification := range e.Claude.Verification {
			if sameCommand(verification.Command, ac.Verification) && verification.Status == "passed" {
				values = append(values, fmt.Sprintf("%s passed", verification.Command))
			}
		}
		evidence = append(evidence, AcceptanceEvidence{ACID: ac.ID, Evidence: values})
	}
	return evidence
}

func findingsFromGaps(acceptanceGaps, qualityFindings []string) []Finding {
	var findings []Finding
	for _, gap := range acceptanceGaps {
		findings = append(findings, Finding{
			Severity:         "medium",
			Category:         "acceptance",
			Summary:          gap,
			BlocksAcceptance: true,
		})
	}
	for _, finding := range qualityFindings {
		findings = append(findings, Finding{
			Severity:         "medium",
			Category:         "quality",
			Summary:          finding,
			BlocksAcceptance: true,
		})
	}
	return findings
}

// RenderCorrectiveWorkOrder asks the executor to continue from the current diff.
func RenderCorrectiveWorkOrder(t task.Task, verdict Verdict) string {
	var b strings.Builder
	b.WriteString("# Corrective Work Order\n\n")
	b.WriteString("Continue the same task. The supervisor reviewed the prior attempt and found gaps.\n\n")
	b.WriteString("Build on the current workspace state. Inspect the current diff and repository state, then fix the listed gaps while preserving correct work already present.\n\n")
	fmt.Fprintf(&b, "Task ID: `%s`\n\n", t.ID)
	fmt.Fprintf(&b, "## Goal\n\n%s\n\n", t.Goal)
	if len(t.RevisionRequests) > 0 {
		b.WriteString("## Pending Revision Requests\n\n")
		for _, request := range t.RevisionRequests {
			if request.Status == "addressed" {
				continue
			}
			fmt.Fprintf(&b, "- `%s`: %s\n", request.ID, request.Text)
		}
		b.WriteString("\n")
	}
	if len(verdict.AcceptanceGaps) > 0 {
		b.WriteString("## Acceptance Gaps\n\n")
		for _, gap := range verdict.AcceptanceGaps {
			fmt.Fprintf(&b, "- %s\n", gap)
		}
		b.WriteString("\n")
	}
	if len(verdict.QualityFindings) > 0 {
		b.WriteString("## Quality Findings\n\n")
		for _, finding := range verdict.QualityFindings {
			fmt.Fprintf(&b, "- %s\n", finding)
		}
		b.WriteString("\n")
	}
	if len(verdict.Findings) > 0 {
		b.WriteString("## Structured Findings\n\n")
		for _, finding := range verdict.Findings {
			location := finding.File
			if location == "" {
				location = "n/a"
			}
			fmt.Fprintf(&b, "- `%s` `%s` `%s`: %s\n", finding.Severity, finding.Category, location, finding.Summary)
		}
		b.WriteString("\n")
	}
	b.WriteString("## Required Behavior\n\n")
	b.WriteString("- Preserve correct work already present.\n")
	b.WriteString("- Fix each listed gap.\n")
	b.WriteString("- Run focused verification for changed behavior.\n")
	b.WriteString("- Update decisions and risks if your previous assumptions change.\n")
	b.WriteString("- Return one JSON object as the entire response body.\n")
	b.WriteString("- Use status `completed`, `completed_with_risks`, or `hard_stop`.\n")
	b.WriteString("- Use acceptance status `satisfied`, `partially_satisfied`, or `not_satisfied`.\n")
	return b.String()
}
