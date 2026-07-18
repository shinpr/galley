package supervisor

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/shinpr/galley/internal/profile"
	"github.com/shinpr/galley/internal/task"
)

func TestValidateVerdictRejectsNeedsRevisionWithoutActionableHandoff(t *testing.T) {
	err := ValidateVerdict(Verdict{Status: "needs_revision", Summary: "gaps", Confidence: "high"})
	if err == nil {
		t.Fatal("expected validation error")
	}
}

func TestValidateVerdictAcceptsFindingBackedNeedsRevisionWithoutWorkOrder(t *testing.T) {
	err := ValidateVerdict(Verdict{
		Status:     "needs_revision",
		Summary:    "gaps",
		Confidence: "high",
		Findings: []Finding{{
			Severity:   "high",
			Category:   "acceptance",
			Summary:    "The saved state omits the requested value; preserve it and verify the round trip.",
			Supersedes: []string{},
		}},
	})
	if err != nil {
		t.Fatalf("finding-backed handoff was rejected: %v", err)
	}
}

func TestValidateVerdictValidatesFindingSupersedes(t *testing.T) {
	tests := []struct {
		name       string
		supersedes []string
		wantErr    string
	}{
		{name: "missing", wantErr: "supersedes is required"},
		{name: "empty", supersedes: []string{}},
		{name: "revision id", supersedes: []string{"revision:supervisor-attempt-1-finding-1"}},
		{name: "acceptance id", supersedes: []string{"AC1"}, wantErr: "must be a revision:<id> string"},
		{name: "empty revision id", supersedes: []string{"revision: "}, wantErr: "must be a revision:<id> string"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateVerdict(Verdict{
				Status: "needs_revision", Summary: "one repair remains", Confidence: "high",
				Findings: []Finding{{
					Severity: "medium", Category: "acceptance", Summary: "repair and verify the boundary", Supersedes: tt.supersedes,
				}},
			})
			if tt.wantErr == "" && err != nil {
				t.Fatalf("ValidateVerdict: %v", err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestValidateVerdictRequiresConfidence(t *testing.T) {
	err := ValidateVerdict(Verdict{Status: "accepted", Summary: "ok"})
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "confidence") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSupervisorVerdictSchemaStatusEnumMatchesValidator(t *testing.T) {
	schemaPath := filepath.Join("..", "..", "schemas", "supervisor-verdict.schema.json")
	data, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Properties map[string]struct {
			Enum []string `json:"enum"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	if _, ok := schema.Properties["quality_passes"]; !ok {
		t.Fatalf("schema missing quality_passes property")
	}
	if _, ok := schema.Properties["quality_gaps"]; !ok {
		t.Fatalf("schema missing quality_gaps property")
	}
	if !slices.Contains(schema.Required, "quality_passes") || !slices.Contains(schema.Required, "quality_gaps") {
		t.Fatalf("schema required = %v", schema.Required)
	}
	got := append([]string(nil), schema.Properties["status"].Enum...)
	for _, status := range got {
		if err := ValidateVerdict(Verdict{Status: status, Summary: "ok", Confidence: "high", NextWorkOrder: "work"}); err != nil && status != "needs_revision" {
			t.Fatalf("validator rejected schema status %q: %v", status, err)
		}
		if status == "needs_revision" {
			if err := ValidateVerdict(Verdict{Status: status, Summary: "ok", Confidence: "high", NextWorkOrder: "work"}); err != nil {
				t.Fatalf("validator rejected needs_revision with work order: %v", err)
			}
		}
	}
}

func TestSupervisorVerdictSchemaRequiresValidFindingSupersedes(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "schemas", "supervisor-verdict.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Properties map[string]struct {
			Items struct {
				Required   []string `json:"required"`
				Properties map[string]struct {
					Items struct {
						Pattern string `json:"pattern"`
					} `json:"items"`
				} `json:"properties"`
			} `json:"items"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	finding := schema.Properties["findings"].Items
	if !slices.Contains(finding.Required, "supersedes") {
		t.Fatalf("finding required = %v", finding.Required)
	}
	pattern, err := regexp.Compile(finding.Properties["supersedes"].Items.Pattern)
	if err != nil {
		t.Fatal(err)
	}
	for value, want := range map[string]bool{
		"revision:supervisor-attempt-1-finding-1": true,
		"AC1":        false,
		"revision: ": false,
	} {
		if got := pattern.MatchString(value); got != want {
			t.Errorf("pattern match %q = %t, want %t", value, got, want)
		}
	}
}

func TestValidateVerdictForEvidenceRejectsAcceptedWithoutRepositoryReview(t *testing.T) {
	err := ValidateVerdictForEvidence(Verdict{
		Status:             "accepted",
		Summary:            "ok",
		AcceptanceEvidence: []AcceptanceEvidence{{ACID: "AC1", Evidence: []string{"diff"}}},
		Confidence:         "high",
		QualityPasses:      []string{},
		QualityGaps:        []string{},
	}, Evidence{
		Task:      task.Task{AcceptanceCriteria: []task.AcceptanceCriterion{{ID: "AC1", Text: "done"}}},
		DiffDirty: true,
	})
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "reviewed_files") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateVerdictForEvidenceRejectsAcceptedGapForAddressedRevision(t *testing.T) {
	t.Parallel()
	err := ValidateVerdictForEvidence(Verdict{
		Status:         "accepted",
		Summary:        "ok",
		AcceptanceGaps: []string{"revision:review-1"},
		QualityPasses:  []string{},
		QualityGaps:    []string{},
		Confidence:     "high",
	}, Evidence{Task: task.Task{RevisionRequests: []task.RevisionRequest{{
		ID:       "review-1",
		Status:   "addressed",
		Evidence: "fixed earlier",
	}}}})
	if err == nil || !strings.Contains(err.Error(), "revision:review-1") {
		t.Fatalf("accepted verdict reused an addressed revision as a gap: %v", err)
	}
}

func TestVerdictJSONPreservesSchemaRequiredEmptyFields(t *testing.T) {
	t.Parallel()
	data, err := json.Marshal(Verdict{
		Status:             "accepted",
		Summary:            "ok",
		AcceptanceGaps:     []string{},
		ReviewedFiles:      []string{},
		AcceptanceEvidence: []AcceptanceEvidence{},
		QualityPasses:      []string{},
		QualityGaps:        []string{},
		Findings: []Finding{{
			Severity: "low", Category: "style", File: "", Summary: "note", Supersedes: []string{},
		}},
		ResidualRisks:   []string{},
		DiscussionItems: []DiscussionItem{},
		Confidence:      "high",
		NextWorkOrder:   "",
	})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{
		"acceptance_gaps", "reviewed_files", "acceptance_evidence", "quality_passes", "quality_gaps",
		"findings", "residual_risks", "discussion_items", "confidence", "next_work_order",
	} {
		if _, ok := got[field]; !ok {
			t.Errorf("schema-required field %q omitted from %s", field, data)
		}
	}
	findings, ok := got["findings"].([]any)
	if !ok || len(findings) != 1 {
		t.Fatalf("findings = %#v", got["findings"])
	}
	finding, ok := findings[0].(map[string]any)
	if !ok {
		t.Fatalf("finding = %#v", findings[0])
	}
	if value, ok := finding["file"]; !ok || value != "" {
		t.Fatalf("schema-required finding.file omitted or changed: %#v", finding)
	}
}

func TestValidateVerdictForEvidenceRejectsAcceptedWithoutACEvidence(t *testing.T) {
	err := ValidateVerdictForEvidence(Verdict{
		Status:        "accepted",
		Summary:       "ok",
		ReviewedFiles: []string{"file.go"},
		Confidence:    "high",
		QualityPasses: []string{},
		QualityGaps:   []string{},
	}, Evidence{
		Task:      task.Task{AcceptanceCriteria: []task.AcceptanceCriterion{{ID: "AC1", Text: "done"}}},
		DiffDirty: true,
	})
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "AC1") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateVerdictForEvidenceRejectsAcceptedWithoutQualityResult(t *testing.T) {
	t.Parallel()
	err := ValidateVerdictForEvidence(Verdict{
		Status:             "accepted",
		Summary:            "ok",
		ReviewedFiles:      []string{"file.go"},
		AcceptanceEvidence: []AcceptanceEvidence{{ACID: "AC1", Evidence: []string{"diff"}}},
		Confidence:         "high",
		QualityGaps:        []string{},
		QualityPasses:      []string{},
	}, Evidence{
		Task: task.Task{AcceptanceCriteria: []task.AcceptanceCriterion{{ID: "AC1", Text: "done"}}},
		Profiles: profile.Bundle{Quality: &profile.Quality{ReviewDimensions: []profile.ReviewDimension{{
			ID: "criterion-a", Weight: 1, Required: true,
		}}}},
		DiffDirty: true,
	})
	if err == nil || !strings.Contains(err.Error(), "criterion-a") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateVerdictForEvidenceAcceptsCompleteQualityResults(t *testing.T) {
	t.Parallel()
	quality := &profile.Quality{
		ReviewDimensions: []profile.ReviewDimension{
			{ID: "criterion-a", Weight: 2, Required: true},
			{ID: "criterion-b", Weight: 1, Required: true},
		},
		PassPolicy: profile.PassPolicy{RequiredDimensionsMustPass: true, MinScore: 100},
	}
	err := ValidateVerdictForEvidence(Verdict{
		Status:             "accepted",
		Summary:            "ok",
		ReviewedFiles:      []string{"file.go"},
		AcceptanceEvidence: []AcceptanceEvidence{{ACID: "AC1", Evidence: []string{"diff"}}},
		QualityGaps:        []string{},
		QualityPasses:      []string{"criterion-a", "criterion-b"},
		Confidence:         "high",
	}, Evidence{
		Task:      task.Task{AcceptanceCriteria: []task.AcceptanceCriterion{{ID: "AC1", Text: "done"}}},
		Profiles:  profile.Bundle{Quality: quality},
		DiffDirty: true,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestValidateVerdictForEvidenceRejectsAcceptedWithoutQualityOutcomeList(t *testing.T) {
	t.Parallel()
	quality := &profile.Quality{ReviewDimensions: []profile.ReviewDimension{{ID: "criterion-a", Weight: 1, Required: true}}}
	err := ValidateVerdictForEvidence(Verdict{
		Status:             "accepted",
		Summary:            "ok",
		ReviewedFiles:      []string{"file.go"},
		AcceptanceEvidence: []AcceptanceEvidence{{ACID: "AC1", Evidence: []string{"diff"}}},
		Confidence:         "high",
	}, Evidence{
		Task:      task.Task{AcceptanceCriteria: []task.AcceptanceCriterion{{ID: "AC1", Text: "done"}}},
		Profiles:  profile.Bundle{Quality: quality},
		DiffDirty: true,
	})
	if err == nil || !strings.Contains(err.Error(), "quality_gaps") {
		t.Fatalf("accepted verdict without explicit quality outcomes: %v", err)
	}
}

func TestValidateVerdictForEvidenceAllowsNeedsRevisionWithoutCompleteQualityResults(t *testing.T) {
	t.Parallel()
	err := ValidateVerdictForEvidence(Verdict{Status: "needs_revision", Summary: "gap", Confidence: "high", NextWorkOrder: "fix it"}, Evidence{
		Profiles: profile.Bundle{Quality: &profile.Quality{ReviewDimensions: []profile.ReviewDimension{{ID: "criterion-a"}}}},
	})
	if err != nil {
		t.Fatalf("incomplete needs_revision results must preserve the handoff: %v", err)
	}
}

func TestValidateVerdictForEvidenceRequiresOnlyOpenReviewItems(t *testing.T) {
	t.Parallel()
	quality := &profile.Quality{ReviewDimensions: []profile.ReviewDimension{
		{ID: "quality-a", Weight: 1, Required: true, Pass: "a"},
		{ID: "quality-b", Weight: 1, Required: true, Pass: "b"},
	}}
	loaded := task.Task{
		AcceptanceCriteria: []task.AcceptanceCriterion{
			{ID: "AC1", Text: "first", Verification: "verify first"},
			{ID: "AC2", Text: "second", Verification: "verify second"},
		},
	}
	ApplyReviewProgress(&loaded, profile.Bundle{Quality: quality}, Verdict{
		AcceptanceEvidence: []AcceptanceEvidence{{ACID: "AC1", Evidence: []string{"first evidence"}}},
		AcceptanceGaps:     []string{"AC2"},
		QualityPasses:      []string{"quality-a"},
		QualityGaps:        []string{"quality-b"},
		Findings:           []Finding{{Category: "quality-b", Summary: "b fails", Supersedes: []string{}}},
	})

	err := ValidateVerdictForEvidence(Verdict{
		Status:             "needs_revision",
		Summary:            "remaining gaps",
		AcceptanceGaps:     []string{"AC2"},
		AcceptanceEvidence: []AcceptanceEvidence{},
		QualityGaps:        []string{"quality-b"},
		Findings:           []Finding{{Severity: "medium", Category: "quality-b", Summary: "b still fails", Supersedes: []string{}}},
		Confidence:         "high",
		NextWorkOrder:      "fix AC2 and quality-b",
	}, Evidence{Task: loaded, Profiles: profile.Bundle{Quality: quality}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestValidateVerdictForEvidenceAllowsMissingOpenAcceptanceResult(t *testing.T) {
	t.Parallel()
	loaded := task.Task{AcceptanceCriteria: []task.AcceptanceCriterion{
		{ID: "AC1", Text: "first", Verification: "verify first"},
		{ID: "AC2", Text: "second", Verification: "verify second"},
	}}
	ApplyReviewProgress(&loaded, profile.Bundle{}, Verdict{
		AcceptanceEvidence: []AcceptanceEvidence{{ACID: "AC1", Evidence: []string{"first evidence"}}},
		AcceptanceGaps:     []string{"AC2"},
	})

	err := ValidateVerdictForEvidence(Verdict{
		Status:        "needs_revision",
		Summary:       "remaining gap",
		Confidence:    "high",
		NextWorkOrder: "fix AC2",
	}, Evidence{Task: loaded})
	if err != nil {
		t.Fatalf("missing needs_revision result must remain open instead of rejecting the handoff: %v", err)
	}
}

func TestValidateVerdictForEvidenceAllowsUnknownNeedsRevisionAcceptanceResult(t *testing.T) {
	t.Parallel()
	err := ValidateVerdictForEvidence(Verdict{
		Status:         "needs_revision",
		Summary:        "remaining gap",
		AcceptanceGaps: []string{"AC1 still fails"},
		Confidence:     "high",
		NextWorkOrder:  "fix AC1",
	}, Evidence{Task: task.Task{AcceptanceCriteria: []task.AcceptanceCriterion{{ID: "AC1", Text: "first", Verification: "verify"}}}})
	if err != nil {
		t.Fatalf("unknown non-accepted result must not discard the work order: %v", err)
	}
}

func TestValidateVerdictForEvidenceAcceptsFinalOpenItemsWithPriorPasses(t *testing.T) {
	t.Parallel()
	quality := &profile.Quality{
		ReviewDimensions: []profile.ReviewDimension{
			{ID: "quality-a", Weight: 1, Required: true, Pass: "a"},
			{ID: "quality-b", Weight: 1, Required: true, Pass: "b"},
		},
		PassPolicy: profile.PassPolicy{RequiredDimensionsMustPass: true, MinScore: 100},
	}
	loaded := task.Task{AcceptanceCriteria: []task.AcceptanceCriterion{
		{ID: "AC1", Text: "first", Verification: "verify first"},
		{ID: "AC2", Text: "second", Verification: "verify second"},
	}}
	ApplyReviewProgress(&loaded, profile.Bundle{Quality: quality}, Verdict{
		AcceptanceEvidence: []AcceptanceEvidence{{ACID: "AC1", Evidence: []string{"first evidence"}}},
		AcceptanceGaps:     []string{"AC2"},
		QualityPasses:      []string{"quality-a"},
		QualityGaps:        []string{"quality-b"},
		Findings:           []Finding{{Category: "quality-b", Summary: "b fails", Supersedes: []string{}}},
	})

	err := ValidateVerdictForEvidence(Verdict{
		Status:             "accepted",
		Summary:            "all done",
		ReviewedFiles:      []string{"b.go"},
		AcceptanceEvidence: []AcceptanceEvidence{{ACID: "AC2", Evidence: []string{"second evidence"}}},
		QualityPasses:      []string{"quality-b"},
		QualityGaps:        []string{},
		Confidence:         "high",
	}, Evidence{Task: loaded, Profiles: profile.Bundle{Quality: quality}, DiffDirty: true})
	if err != nil {
		t.Fatal(err)
	}
}

func TestValidateVerdictForEvidenceAllowsIncompleteQualityResultsForTerminalEscalation(t *testing.T) {
	t.Parallel()
	quality := &profile.Quality{ReviewDimensions: []profile.ReviewDimension{{ID: "criterion-a"}}}
	for _, status := range []string{"hard_stop", "needs_supervisor_review"} {
		status := status
		t.Run(status, func(t *testing.T) {
			t.Parallel()
			err := ValidateVerdictForEvidence(Verdict{Status: status, Summary: "external decision required", Confidence: "high"}, Evidence{Profiles: profile.Bundle{Quality: quality}})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestValidateVerdictForEvidenceAllowsMalformedQualityResultsForTerminalEscalation(t *testing.T) {
	t.Parallel()
	quality := &profile.Quality{ReviewDimensions: []profile.ReviewDimension{{ID: "criterion-a"}}}
	for _, status := range []string{"hard_stop", "needs_supervisor_review"} {
		status := status
		t.Run(status, func(t *testing.T) {
			t.Parallel()
			err := ValidateVerdictForEvidence(Verdict{
				Status: status, Summary: "external decision required", Confidence: "high",
				QualityPasses: []string{"unknown"},
			}, Evidence{Profiles: profile.Bundle{Quality: quality}})
			if err != nil {
				t.Fatalf("terminal handoff must not be discarded over partial review results: %v", err)
			}
		})
	}
}

func TestValidateQualityResultsRejectsInvalidEntries(t *testing.T) {
	t.Parallel()
	quality := &profile.Quality{ReviewDimensions: []profile.ReviewDimension{{ID: "criterion-a"}}}
	tests := []struct {
		name   string
		passes []string
		gaps   []string
		want   string
	}{
		{name: "unknown pass", passes: []string{"criterion-b"}, want: "not configured"},
		{name: "unknown gap", gaps: []string{"criterion-b"}, want: "not configured"},
		{name: "duplicate pass", passes: []string{"criterion-a", "criterion-a"}, want: "multiple"},
		{name: "pass and gap", passes: []string{"criterion-a"}, gaps: []string{"criterion-a"}, want: "both"},
		{name: "missing required result", want: "no pass or gap"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateQualityResults(tt.passes, tt.gaps, quality, []string{"criterion-a"})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestValidateVerdictForEvidenceAcceptsQualityPass(t *testing.T) {
	t.Parallel()
	quality := &profile.Quality{ReviewDimensions: []profile.ReviewDimension{{ID: "criterion-a"}}}
	err := ValidateVerdictForEvidence(Verdict{
		Status: "accepted", Summary: "ok", Confidence: "high", ReviewedFiles: []string{"file.go"},
		QualityPasses: []string{"criterion-a"},
		QualityGaps:   []string{},
	}, Evidence{Profiles: profile.Bundle{Quality: quality}, DiffDirty: true})
	if err != nil {
		t.Fatal(err)
	}
}

func TestValidateVerdictForEvidenceRejectsFailedRequiredQualityDimension(t *testing.T) {
	t.Parallel()
	quality := &profile.Quality{
		ReviewDimensions: []profile.ReviewDimension{{ID: "criterion-a", Weight: 1, Required: true}},
		PassPolicy:       profile.PassPolicy{RequiredDimensionsMustPass: true},
	}
	err := ValidateVerdictForEvidence(Verdict{
		Status:             "accepted",
		Summary:            "ok",
		ReviewedFiles:      []string{"file.go"},
		AcceptanceEvidence: []AcceptanceEvidence{{ACID: "AC1", Evidence: []string{"diff"}}},
		QualityPasses:      []string{},
		QualityGaps:        []string{"criterion-a"},
		Findings:           []Finding{{Severity: "low", Category: "criterion-a", Summary: "criterion is not satisfied", BlocksAcceptance: false, Supersedes: []string{}}},
		Confidence:         "high",
	}, Evidence{
		Task:      task.Task{AcceptanceCriteria: []task.AcceptanceCriterion{{ID: "AC1", Text: "done"}}},
		Profiles:  profile.Bundle{Quality: quality},
		DiffDirty: true,
	})
	if err == nil || !strings.Contains(err.Error(), "required quality criterion") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateVerdictForEvidenceUsesQualityGapsInsteadOfFindingCategory(t *testing.T) {
	t.Parallel()
	quality := &profile.Quality{
		ReviewDimensions: []profile.ReviewDimension{{ID: "criterion-a", Weight: 1, Required: true}},
		PassPolicy:       profile.PassPolicy{RequiredDimensionsMustPass: true},
	}
	err := ValidateVerdictForEvidence(Verdict{
		Status:        "accepted",
		Summary:       "quality criterion passed with a non-blocking observation",
		QualityPasses: []string{"criterion-a"},
		QualityGaps:   []string{},
		Findings:      []Finding{{Severity: "low", Category: "criterion-a", Summary: "non-blocking observation", BlocksAcceptance: false, Supersedes: []string{}}},
		Confidence:    "high",
	}, Evidence{Profiles: profile.Bundle{Quality: quality}})
	if err != nil {
		t.Fatalf("quality finding category overrode explicit quality_gaps: %v", err)
	}
}

func TestValidateVerdictForEvidenceEnforcesQualityScore(t *testing.T) {
	t.Parallel()
	quality := &profile.Quality{
		ReviewDimensions: []profile.ReviewDimension{
			{ID: "criterion-a", Weight: 1},
			{ID: "criterion-b", Weight: 3},
		},
		PassPolicy: profile.PassPolicy{MinScore: 50},
	}
	err := ValidateVerdictForEvidence(Verdict{
		Status:             "accepted",
		Summary:            "ok",
		ReviewedFiles:      []string{"file.go"},
		AcceptanceEvidence: []AcceptanceEvidence{{ACID: "AC1", Evidence: []string{"diff"}}},
		QualityPasses:      []string{"criterion-a"},
		QualityGaps:        []string{"criterion-b"},
		Findings:           []Finding{{Severity: "low", Category: "criterion-b", Summary: "criterion gap", BlocksAcceptance: false, Supersedes: []string{}}},
		Confidence:         "high",
	}, Evidence{
		Task:      task.Task{AcceptanceCriteria: []task.AcceptanceCriterion{{ID: "AC1", Text: "done"}}},
		Profiles:  profile.Bundle{Quality: quality},
		DiffDirty: true,
	})
	if err == nil || !strings.Contains(err.Error(), "quality score 25") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestQualityPassScoreReturnsFullScoreWhenTotalWeightIsZero(t *testing.T) {
	t.Parallel()
	quality := &profile.Quality{ReviewDimensions: []profile.ReviewDimension{{ID: "criterion-a", Weight: 0}}}
	if got := qualityPassScore(map[string]bool{}, quality); got != 100 {
		t.Fatalf("score = %d, want 100", got)
	}
}

func TestValidateVerdictForEvidenceRejectsAcceptedWithBlockingFinding(t *testing.T) {
	err := ValidateVerdictForEvidence(Verdict{
		Status:             "accepted",
		Summary:            "ok",
		ReviewedFiles:      []string{"file.go"},
		AcceptanceEvidence: []AcceptanceEvidence{{ACID: "AC1", Evidence: []string{"diff"}}},
		Findings:           []Finding{{Severity: "medium", Category: "correctness", Summary: "ordering bug", BlocksAcceptance: false, Supersedes: []string{}}},
		Confidence:         "high",
	}, Evidence{
		Task:      task.Task{AcceptanceCriteria: []task.AcceptanceCriterion{{ID: "AC1", Text: "done"}}},
		DiffDirty: true,
	})
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "blocks_acceptance") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateVerdictForEvidenceDefaultAllowsLowSeverityFinding(t *testing.T) {
	err := ValidateVerdictForEvidence(Verdict{
		Status:             "accepted",
		Summary:            "ok",
		ReviewedFiles:      []string{"file.go"},
		AcceptanceEvidence: []AcceptanceEvidence{{ACID: "AC1", Evidence: []string{"diff"}}},
		Findings:           []Finding{{Severity: "low", Category: "style", Summary: "wording", BlocksAcceptance: false, Supersedes: []string{}}},
		Confidence:         "medium",
	}, Evidence{
		Task:      task.Task{AcceptanceCriteria: []task.AcceptanceCriterion{{ID: "AC1", Text: "done"}}},
		DiffDirty: true,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestValidateVerdictForEvidenceBlockingSeveritiesCanRequireLowSeverityFinding(t *testing.T) {
	err := ValidateVerdictForEvidence(Verdict{
		Status:             "accepted",
		Summary:            "ok",
		ReviewedFiles:      []string{"file.go"},
		AcceptanceEvidence: []AcceptanceEvidence{{ACID: "AC1", Evidence: []string{"diff"}}},
		Findings:           []Finding{{Severity: "low", Category: "style", Summary: "wording", BlocksAcceptance: false, Supersedes: []string{}}},
		Confidence:         "medium",
	}, Evidence{
		Task: task.Task{AcceptanceCriteria: []task.AcceptanceCriterion{{ID: "AC1", Text: "done"}}},
		Profiles: profile.Bundle{Quality: &profile.Quality{PassPolicy: profile.PassPolicy{
			BlockingSeverities: []string{"critical", "high", "medium", "low"},
		}}},
		DiffDirty: true,
	})
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "blocks_acceptance") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateVerdictForEvidenceRejectsBlocksAcceptanceMismatch(t *testing.T) {
	err := ValidateVerdictForEvidence(Verdict{
		Status:             "accepted",
		Summary:            "ok",
		ReviewedFiles:      []string{"file.go"},
		AcceptanceEvidence: []AcceptanceEvidence{{ACID: "AC1", Evidence: []string{"diff"}}},
		Findings:           []Finding{{Severity: "low", Category: "style", Summary: "wording", BlocksAcceptance: true, Supersedes: []string{}}},
		Confidence:         "medium",
	}, Evidence{
		Task:      task.Task{AcceptanceCriteria: []task.AcceptanceCriterion{{ID: "AC1", Text: "done"}}},
		DiffDirty: true,
	})
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "blocks_acceptance") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateVerdictForEvidenceAllowsBlocksAcceptanceMismatchOnNeedsRevision(t *testing.T) {
	// A non-accepted verdict must not be rejected over its findings' blocks_acceptance
	// flag: the flag is moot when the verdict is already needs_revision, and rejecting
	// here would discard actionable next_work_order feedback and stall the AFK loop.
	err := ValidateVerdictForEvidence(Verdict{
		Status:         "needs_revision",
		Summary:        "needs work",
		NextWorkOrder:  "fix the failing test",
		AcceptanceGaps: []string{"AC1"},
		Findings:       []Finding{{Severity: "medium", Category: "correctness", Summary: "bug", BlocksAcceptance: false, Supersedes: []string{}}},
		Confidence:     "medium",
	}, Evidence{
		Task: task.Task{AcceptanceCriteria: []task.AcceptanceCriterion{{ID: "AC1", Text: "done"}}},
		Profiles: profile.Bundle{Quality: &profile.Quality{PassPolicy: profile.PassPolicy{
			BlockingSeverities: []string{"critical", "high", "medium"},
		}}},
		DiffDirty: true,
	})
	if err != nil {
		t.Fatalf("expected no error for needs_revision verdict, got: %v", err)
	}
}

func TestValidateVerdictForEvidenceIgnoresDiscussionItemsOnNeedsRevision(t *testing.T) {
	evidence := Evidence{
		Task: task.Task{AcceptanceCriteria: []task.AcceptanceCriterion{{ID: "AC1", Text: "done"}}},
	}
	accepted := Verdict{
		Status:             "accepted",
		Summary:            "ok",
		ReviewedFiles:      []string{"README.md"},
		AcceptanceEvidence: []AcceptanceEvidence{{ACID: "AC1", Evidence: []string{"diff"}}},
		DiscussionItems:    []DiscussionItem{{Topic: "AC wording", Summary: "Future tasks could clarify value coercion."}},
		Confidence:         "medium",
	}
	if err := ValidateVerdictForEvidence(accepted, evidence); err != nil {
		t.Fatalf("accepted discussion item rejected: %v", err)
	}
	revision := Verdict{
		Status:          "needs_revision",
		Summary:         "fix it",
		DiscussionItems: []DiscussionItem{{Topic: "AC wording", Summary: "Not relevant before acceptance."}},
		Confidence:      "medium",
		NextWorkOrder:   "fix it",
	}
	if err := ValidateVerdictForEvidence(revision, evidence); err != nil {
		t.Fatalf("non-accepted optional discussion item must not discard the handoff: %v", err)
	}
}

func TestNewAdapterRequestSerializesErrorsAsStrings(t *testing.T) {
	request := NewAdapterRequest(Evidence{
		ParseError:   errors.New("bad json"),
		RunError:     errors.New("executor failed"),
		DiffError:    errors.New("diff failed"),
		Diff:         "diff --git a/file b/file",
		DiffDirty:    true,
		Attempt:      2,
		AttemptsLeft: 1,
		Task: task.Task{Scope: task.Scope{
			CWD: "/source/repo",
		}},
	})
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{
		`"parse_error":"bad json"`,
		`"run_error":"executor failed"`,
		`"diff_error":"diff failed"`,
		`"diff_dirty":true`,
		`"source_cwd":"/source/repo"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("request JSON missing %s: %s", want, text)
		}
	}
	if strings.Contains(text, `"ParseError"`) || strings.Contains(text, `"RunError"`) {
		t.Fatalf("request leaked Go field names: %s", text)
	}
}

func TestNewAdapterRequestUsesReviewContractSourceCWD(t *testing.T) {
	t.Parallel()
	request := NewAdapterRequest(Evidence{
		Task: task.Task{Scope: task.Scope{CWD: "/worktrees/task-1"}},
		ReviewContractContext: ReviewContractContext{
			SourceCWD: "/source/repo",
		},
	})
	if got := request.Evidence.SourceCWD; got != "/source/repo" {
		t.Fatalf("source_cwd = %q, want original repository path", got)
	}
}
