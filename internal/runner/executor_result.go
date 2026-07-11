package runner

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"strings"
)

// ExecutorResult is the provider-neutral structured result returned by an executor.
type ExecutorResult struct {
	Status             string                        `json:"status"`
	Summary            string                        `json:"summary"`
	FilesModified      []string                      `json:"files_modified"`
	AcceptanceCriteria []ExecutorAcceptanceCriterion `json:"acceptance_criteria"`
	Verification       []ExecutorVerification        `json:"verification"`
	ScopeExpansions    []ExecutorScopeExpansion      `json:"scope_expansions"`
	Decisions          []ExecutorDecision            `json:"decisions"`
	Risks              []ExecutorRisk                `json:"risks"`
	HardStop           *ExecutorHardStop             `json:"hard_stop,omitempty"`
}

// ExecutorAcceptanceCriterion records executor evidence for one criterion.
type ExecutorAcceptanceCriterion struct {
	ID       string   `json:"id"`
	Status   string   `json:"status"`
	Evidence []string `json:"evidence"`
	Notes    string   `json:"notes"`
}

// ExecutorVerification records one verification command reported by an executor.
type ExecutorVerification struct {
	Command       string `json:"command"`
	Status        string `json:"status"`
	Reason        string `json:"reason"`
	OutputExcerpt string `json:"output_excerpt"`
}

// ExecutorScopeExpansion records a deliberate change outside task.scope.allowed_paths.
type ExecutorScopeExpansion struct {
	Path              string `json:"path"`
	Reason            string `json:"reason"`
	LinkedRequirement string `json:"linked_requirement"`
	Minimality        string `json:"minimality"`
}

// ExecutorDecision records one decision reported by an executor.
type ExecutorDecision struct {
	Question         string `json:"question"`
	Chosen           string `json:"chosen"`
	Rationale        string `json:"rationale"`
	Reversibility    string `json:"reversibility"`
	NeedsHumanReview bool   `json:"needs_human_review"`
}

// ExecutorRisk records one risk reported by an executor.
type ExecutorRisk struct {
	Type             string `json:"type"`
	Detail           string `json:"detail"`
	Mitigation       string `json:"mitigation"`
	NeedsHumanReview bool   `json:"needs_human_review"`
}

// ExecutorHardStop records hard-stop details.
type ExecutorHardStop struct {
	Reason           string   `json:"reason"`
	Attempted        []string `json:"attempted"`
	NeededToContinue []string `json:"needed_to_continue"`
}

// ExtractExecutorResult parses a provider-neutral structured result from text.
func ExtractExecutorResult(text string) (ExecutorResult, error) {
	if result, found, err := extractExecutorResultLine(strings.TrimSpace(text)); found {
		return result, err
	}
	var firstErr error
	for _, line := range strings.Split(text, "\n") {
		result, found, err := extractExecutorResultLine(strings.TrimSpace(line))
		if found && err == nil {
			return result, nil
		}
		if found && firstErr == nil && err != nil {
			firstErr = err
		}
	}
	if firstErr != nil {
		return ExecutorResult{}, firstErr
	}
	return ExecutorResult{}, fmt.Errorf("structured executor result not found")
}

// ExtractExecutorResultFile parses structured executor results from a text file.
func ExtractExecutorResultFile(path string) (ExecutorResult, error) {
	file, err := os.Open(path)
	if err != nil {
		return ExecutorResult{}, fmt.Errorf("open executor output %s: %w", path, err)
	}
	defer file.Close()

	reader := bufio.NewReader(file)
	var firstErr error
	for {
		line, readErr := reader.ReadString('\n')
		if line != "" {
			result, found, err := extractExecutorResultLine(strings.TrimSpace(line))
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
		return ExecutorResult{}, firstErr
	}
	return ExecutorResult{}, fmt.Errorf("structured executor result not found")
}

// Validate checks the lightweight executor result contract used by the daemon.
// Empty arrays are valid, but nil slices are rejected because the JSON contract
// requires explicit array fields for stable downstream evidence handling.
func (r ExecutorResult) Validate() error {
	switch r.Status {
	case "completed", "completed_with_risks", "hard_stop":
	default:
		return fmt.Errorf("invalid executor result status %q", r.Status)
	}
	if r.Summary == "" {
		return fmt.Errorf("executor result summary is required")
	}
	if r.FilesModified == nil {
		return fmt.Errorf("executor result files_modified is required")
	}
	if r.AcceptanceCriteria == nil {
		return fmt.Errorf("executor result acceptance_criteria is required")
	}
	if r.Verification == nil {
		return fmt.Errorf("executor result verification is required")
	}
	if r.ScopeExpansions == nil {
		return fmt.Errorf("executor result scope_expansions is required")
	}
	if r.Decisions == nil {
		return fmt.Errorf("executor result decisions is required")
	}
	if r.Risks == nil {
		return fmt.Errorf("executor result risks is required")
	}
	if r.Status == "hard_stop" && r.HardStop == nil {
		return fmt.Errorf("executor hard_stop result requires hard_stop details")
	}
	if r.Status != "hard_stop" && r.HardStop != nil {
		return fmt.Errorf("executor non-hard-stop result must not include hard_stop details")
	}
	for i, ac := range r.AcceptanceCriteria {
		if ac.ID == "" {
			return fmt.Errorf("executor acceptance_criteria[%d].id is required", i)
		}
		switch ac.Status {
		case "satisfied", "partially_satisfied", "not_satisfied":
		default:
			return fmt.Errorf("invalid executor acceptance_criteria[%d].status %q", i, ac.Status)
		}
		if ac.Evidence == nil {
			return fmt.Errorf("executor acceptance_criteria[%d].evidence is required", i)
		}
	}
	for i, verification := range r.Verification {
		if verification.Command == "" {
			return fmt.Errorf("executor verification[%d].command is required", i)
		}
		switch verification.Status {
		case "passed", "failed", "skipped":
		default:
			return fmt.Errorf("invalid executor verification[%d].status %q", i, verification.Status)
		}
		if verification.Reason == "" {
			return fmt.Errorf("executor verification[%d].reason is required", i)
		}
	}
	for i, expansion := range r.ScopeExpansions {
		if expansion.Path == "" {
			return fmt.Errorf("executor scope_expansions[%d].path is required", i)
		}
		if !validScopeExpansionPath(expansion.Path) {
			return fmt.Errorf("executor scope_expansions[%d].path must be a clean relative path", i)
		}
		if expansion.Reason == "" {
			return fmt.Errorf("executor scope_expansions[%d].reason is required", i)
		}
		if expansion.LinkedRequirement == "" {
			return fmt.Errorf("executor scope_expansions[%d].linked_requirement is required", i)
		}
		if expansion.Minimality == "" {
			return fmt.Errorf("executor scope_expansions[%d].minimality is required", i)
		}
	}
	for i, decision := range r.Decisions {
		if decision.Question == "" {
			return fmt.Errorf("executor decisions[%d].question is required", i)
		}
		if decision.Chosen == "" {
			return fmt.Errorf("executor decisions[%d].chosen is required", i)
		}
		if decision.Rationale == "" {
			return fmt.Errorf("executor decisions[%d].rationale is required", i)
		}
		switch decision.Reversibility {
		case "high", "medium", "low":
		default:
			return fmt.Errorf("invalid executor decisions[%d].reversibility %q", i, decision.Reversibility)
		}
	}
	for i, risk := range r.Risks {
		switch risk.Type {
		case "ambiguous_requirement", "partial_verification", "external_dependency", "technical_debt", "other":
		default:
			return fmt.Errorf("invalid executor risks[%d].type %q", i, risk.Type)
		}
		if risk.Detail == "" {
			return fmt.Errorf("executor risks[%d].detail is required", i)
		}
		if risk.Mitigation == "" {
			return fmt.Errorf("executor risks[%d].mitigation is required", i)
		}
	}
	if r.HardStop != nil {
		if r.HardStop.Reason == "" {
			return fmt.Errorf("executor hard_stop.reason is required")
		}
		if r.HardStop.Attempted == nil {
			return fmt.Errorf("executor hard_stop.attempted is required")
		}
		if r.HardStop.NeededToContinue == nil {
			return fmt.Errorf("executor hard_stop.needed_to_continue is required")
		}
	}
	return nil
}

func validScopeExpansionPath(p string) bool {
	if p == "" ||
		strings.HasPrefix(p, "/") ||
		strings.HasPrefix(p, "//") ||
		strings.Contains(p, "\\") ||
		strings.Contains(p, "//") ||
		strings.HasSuffix(p, "/") ||
		hasWindowsDrivePrefix(p) {
		return false
	}
	clean := path.Clean(p)
	return clean != "." && clean == p && !strings.HasPrefix(clean, "../") && clean != ".."
}

func hasWindowsDrivePrefix(p string) bool {
	if len(p) < 2 || p[1] != ':' {
		return false
	}
	c := p[0]
	return ('A' <= c && c <= 'Z') || ('a' <= c && c <= 'z')
}

func parseExecutorResult(text string) (ExecutorResult, bool) {
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start < 0 || end < start {
		return ExecutorResult{}, false
	}
	return parseRawExecutorResult([]byte(text[start : end+1]))
}

func extractExecutorResultLine(line string) (ExecutorResult, bool, error) {
	if line == "" {
		return ExecutorResult{}, false, nil
	}
	if result, ok := parseExecutorResult(line); ok {
		return result, true, result.Validate()
	}
	var event map[string]json.RawMessage
	if err := json.Unmarshal([]byte(line), &event); err != nil {
		return ExecutorResult{}, false, nil
	}
	for _, key := range []string{"result", "response", "message"} {
		raw, ok := event[key]
		if !ok {
			continue
		}
		var text string
		if err := json.Unmarshal(raw, &text); err == nil {
			if result, ok := parseExecutorResult(text); ok {
				return result, true, result.Validate()
			}
		}
		if result, ok := parseRawExecutorResult(raw); ok {
			return result, true, result.Validate()
		}
	}
	return ExecutorResult{}, false, nil
}

func parseRawExecutorResult(data []byte) (ExecutorResult, bool) {
	var result ExecutorResult
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return ExecutorResult{}, false
	}
	if result.Status == "" {
		return ExecutorResult{}, false
	}
	return result, true
}
