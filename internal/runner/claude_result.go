package runner

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// ClaudeResult is the structured executor result returned by Claude.
type ClaudeResult struct {
	Status             string                      `json:"status"`
	Summary            string                      `json:"summary"`
	FilesModified      []string                    `json:"files_modified"`
	AcceptanceCriteria []ClaudeAcceptanceCriterion `json:"acceptance_criteria"`
	Verification       []ClaudeVerification        `json:"verification"`
	Decisions          []ClaudeDecision            `json:"decisions"`
	Risks              []ClaudeRisk                `json:"risks"`
	HardStop           *ClaudeHardStop             `json:"hard_stop,omitempty"`
}

// ClaudeAcceptanceCriterion records Claude's evidence for one criterion.
type ClaudeAcceptanceCriterion struct {
	ID       string   `json:"id"`
	Status   string   `json:"status"`
	Evidence []string `json:"evidence"`
	Notes    string   `json:"notes"`
}

// ClaudeVerification records one verification command reported by Claude.
type ClaudeVerification struct {
	Command       string `json:"command"`
	Status        string `json:"status"`
	Reason        string `json:"reason"`
	OutputExcerpt string `json:"output_excerpt"`
}

// ClaudeDecision records one decision reported by Claude.
type ClaudeDecision struct {
	Question         string `json:"question"`
	Chosen           string `json:"chosen"`
	Rationale        string `json:"rationale"`
	Reversibility    string `json:"reversibility"`
	NeedsHumanReview bool   `json:"needs_human_review"`
}

// ClaudeRisk records one risk reported by Claude.
type ClaudeRisk struct {
	Type             string `json:"type"`
	Detail           string `json:"detail"`
	Mitigation       string `json:"mitigation"`
	NeedsHumanReview bool   `json:"needs_human_review"`
}

// ClaudeHardStop records hard-stop details.
type ClaudeHardStop struct {
	Reason           string   `json:"reason"`
	Attempted        []string `json:"attempted"`
	NeededToContinue []string `json:"needed_to_continue"`
}

// ExtractClaudeResult parses Claude's structured result from stdout.
func ExtractClaudeResult(stdout string) (ClaudeResult, error) {
	if result, found, err := extractClaudeResultLine(strings.TrimSpace(stdout)); found {
		return result, err
	}
	var firstErr error
	for _, line := range strings.Split(stdout, "\n") {
		result, found, err := extractClaudeResultLine(strings.TrimSpace(line))
		if found && err == nil {
			return result, nil
		}
		if found && firstErr == nil && err != nil {
			firstErr = err
		}
	}
	if firstErr != nil {
		return ClaudeResult{}, firstErr
	}
	return ClaudeResult{}, fmt.Errorf("structured Claude result not found")
}

// ExtractClaudeResultFile parses Claude's structured result from a captured stdout file.
func ExtractClaudeResultFile(path string) (ClaudeResult, error) {
	file, err := os.Open(path)
	if err != nil {
		return ClaudeResult{}, fmt.Errorf("open Claude stdout %s: %w", path, err)
	}
	defer file.Close()

	reader := bufio.NewReader(file)
	var firstErr error
	for {
		line, readErr := reader.ReadString('\n')
		if line != "" {
			result, found, err := extractClaudeResultLine(strings.TrimSpace(line))
			if found && err == nil {
				return result, nil
			}
			if found && firstErr == nil && err != nil {
				firstErr = err
			}
		}
		if readErr != nil {
			break
		}
	}
	if firstErr != nil {
		return ClaudeResult{}, firstErr
	}
	return ClaudeResult{}, fmt.Errorf("structured Claude result not found")
}

// Validate checks the lightweight executor result contract used by the daemon.
// Empty arrays are valid, but nil slices are rejected because the JSON contract
// requires explicit array fields for stable downstream evidence handling.
func (r ClaudeResult) Validate() error {
	switch r.Status {
	case "completed", "completed_with_risks", "hard_stop":
	default:
		return fmt.Errorf("invalid Claude result status %q", r.Status)
	}
	if r.Summary == "" {
		return fmt.Errorf("Claude result summary is required")
	}
	if r.FilesModified == nil {
		return fmt.Errorf("Claude result files_modified is required")
	}
	if r.AcceptanceCriteria == nil {
		return fmt.Errorf("Claude result acceptance_criteria is required")
	}
	if r.Verification == nil {
		return fmt.Errorf("Claude result verification is required")
	}
	if r.Decisions == nil {
		return fmt.Errorf("Claude result decisions is required")
	}
	if r.Risks == nil {
		return fmt.Errorf("Claude result risks is required")
	}
	if r.Status == "hard_stop" && r.HardStop == nil {
		return fmt.Errorf("Claude hard_stop result requires hard_stop details")
	}
	if r.Status != "hard_stop" && r.HardStop != nil {
		return fmt.Errorf("Claude non-hard-stop result must not include hard_stop details")
	}
	for i, ac := range r.AcceptanceCriteria {
		if ac.ID == "" {
			return fmt.Errorf("Claude acceptance_criteria[%d].id is required", i)
		}
		switch ac.Status {
		case "satisfied", "partially_satisfied", "not_satisfied":
		default:
			return fmt.Errorf("invalid Claude acceptance_criteria[%d].status %q", i, ac.Status)
		}
		if ac.Evidence == nil {
			return fmt.Errorf("Claude acceptance_criteria[%d].evidence is required", i)
		}
	}
	for i, verification := range r.Verification {
		if verification.Command == "" {
			return fmt.Errorf("Claude verification[%d].command is required", i)
		}
		switch verification.Status {
		case "passed", "failed", "skipped":
		default:
			return fmt.Errorf("invalid Claude verification[%d].status %q", i, verification.Status)
		}
		if verification.Reason == "" {
			return fmt.Errorf("Claude verification[%d].reason is required", i)
		}
	}
	for i, decision := range r.Decisions {
		if decision.Question == "" {
			return fmt.Errorf("Claude decisions[%d].question is required", i)
		}
		if decision.Chosen == "" {
			return fmt.Errorf("Claude decisions[%d].chosen is required", i)
		}
		if decision.Rationale == "" {
			return fmt.Errorf("Claude decisions[%d].rationale is required", i)
		}
		switch decision.Reversibility {
		case "high", "medium", "low":
		default:
			return fmt.Errorf("invalid Claude decisions[%d].reversibility %q", i, decision.Reversibility)
		}
	}
	for i, risk := range r.Risks {
		switch risk.Type {
		case "ambiguous_requirement", "partial_verification", "external_dependency", "technical_debt", "other":
		default:
			return fmt.Errorf("invalid Claude risks[%d].type %q", i, risk.Type)
		}
		if risk.Detail == "" {
			return fmt.Errorf("Claude risks[%d].detail is required", i)
		}
		if risk.Mitigation == "" {
			return fmt.Errorf("Claude risks[%d].mitigation is required", i)
		}
	}
	if r.HardStop != nil {
		if r.HardStop.Reason == "" {
			return fmt.Errorf("Claude hard_stop.reason is required")
		}
		if r.HardStop.Attempted == nil {
			return fmt.Errorf("Claude hard_stop.attempted is required")
		}
		if r.HardStop.NeededToContinue == nil {
			return fmt.Errorf("Claude hard_stop.needed_to_continue is required")
		}
	}
	return nil
}

func parseClaudeResult(text string) (ClaudeResult, bool) {
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start < 0 || end < start {
		return ClaudeResult{}, false
	}
	return parseRawClaudeResult([]byte(text[start : end+1]))
}

func extractClaudeResultLine(line string) (ClaudeResult, bool, error) {
	if line == "" {
		return ClaudeResult{}, false, nil
	}
	if result, ok := parseClaudeResult(line); ok {
		return result, true, result.Validate()
	}
	var event map[string]json.RawMessage
	if err := json.Unmarshal([]byte(line), &event); err != nil {
		return ClaudeResult{}, false, nil
	}
	for _, key := range []string{"result", "response", "message"} {
		raw, ok := event[key]
		if !ok {
			continue
		}
		var text string
		if err := json.Unmarshal(raw, &text); err == nil {
			if result, ok := parseClaudeResult(text); ok {
				return result, true, result.Validate()
			}
		}
		if result, ok := parseRawClaudeResult(raw); ok {
			return result, true, result.Validate()
		}
	}
	return ClaudeResult{}, false, nil
}

func parseRawClaudeResult(data []byte) (ClaudeResult, bool) {
	var result ClaudeResult
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return ClaudeResult{}, false
	}
	if result.Status == "" {
		return ClaudeResult{}, false
	}
	return result, true
}
