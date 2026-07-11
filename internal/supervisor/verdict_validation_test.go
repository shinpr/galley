package supervisor

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/shinpr/galley/internal/profile"
	"github.com/shinpr/galley/internal/task"
)

func TestValidateVerdictRejectsNeedsRevisionWithoutWorkOrder(t *testing.T) {
	err := ValidateVerdict(Verdict{Status: "needs_revision", Summary: "gaps", Confidence: "high", QualityCoverage: []QualityCoverage{}})
	if err == nil {
		t.Fatal("expected validation error")
	}
}

func TestValidateVerdictRequiresConfidence(t *testing.T) {
	err := ValidateVerdict(Verdict{Status: "accepted", Summary: "ok", QualityCoverage: []QualityCoverage{}})
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
	if _, ok := schema.Properties["quality_coverage"]; !ok {
		t.Fatalf("schema missing quality_coverage property")
	}
	if !slices.Contains(schema.Required, "quality_coverage") {
		t.Fatalf("schema required = %v", schema.Required)
	}
	got := append([]string(nil), schema.Properties["status"].Enum...)
	for _, status := range got {
		if err := ValidateVerdict(Verdict{Status: status, Summary: "ok", Confidence: "high", NextWorkOrder: "work", QualityCoverage: []QualityCoverage{}}); err != nil && status != "needs_revision" {
			t.Fatalf("validator rejected schema status %q: %v", status, err)
		}
		if status == "needs_revision" {
			if err := ValidateVerdict(Verdict{Status: status, Summary: "ok", Confidence: "high", NextWorkOrder: "work", QualityCoverage: []QualityCoverage{}}); err != nil {
				t.Fatalf("validator rejected needs_revision with work order: %v", err)
			}
		}
	}
}

func TestValidateVerdictForEvidenceRejectsAcceptedWithoutRepositoryReview(t *testing.T) {
	err := ValidateVerdictForEvidence(Verdict{
		Status:             "accepted",
		Summary:            "ok",
		AcceptanceEvidence: []AcceptanceEvidence{{ACID: "AC1", Evidence: []string{"diff"}}},
		Confidence:         "high",
		QualityCoverage:    []QualityCoverage{},
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

func TestValidateVerdictForEvidenceRejectsAcceptedWithoutACEvidence(t *testing.T) {
	err := ValidateVerdictForEvidence(Verdict{
		Status:          "accepted",
		Summary:         "ok",
		ReviewedFiles:   []string{"file.go"},
		Confidence:      "high",
		QualityCoverage: []QualityCoverage{},
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

func TestValidateVerdictForEvidenceRejectsAcceptedWithoutQualityCoverage(t *testing.T) {
	t.Parallel()
	err := ValidateVerdictForEvidence(Verdict{
		Status:             "accepted",
		Summary:            "ok",
		ReviewedFiles:      []string{"file.go"},
		AcceptanceEvidence: []AcceptanceEvidence{{ACID: "AC1", Evidence: []string{"diff"}}},
		Confidence:         "high",
		QualityCoverage:    []QualityCoverage{},
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

func TestValidateVerdictForEvidenceAcceptsCompleteQualityCoverage(t *testing.T) {
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
		QualityCoverage: []QualityCoverage{
			{Criterion: "criterion-a", ChangedSurface: "file.go", EvidenceChecked: []string{"criterion evidence"}},
			{Criterion: "criterion-b", ChangedSurface: "final diff", EvidenceChecked: []string{"No narrower changed surface is governed by this criterion."}},
		},
		Confidence: "high",
	}, Evidence{
		Task:      task.Task{AcceptanceCriteria: []task.AcceptanceCriterion{{ID: "AC1", Text: "done"}}},
		Profiles:  profile.Bundle{Quality: quality},
		DiffDirty: true,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestValidateVerdictForEvidenceRejectsNonAcceptedWithoutQualityCoverage(t *testing.T) {
	t.Parallel()
	err := ValidateVerdictForEvidence(Verdict{Status: "needs_revision", Summary: "gap", Confidence: "high", NextWorkOrder: "fix it", QualityCoverage: []QualityCoverage{}}, Evidence{
		Profiles: profile.Bundle{Quality: &profile.Quality{ReviewDimensions: []profile.ReviewDimension{{ID: "criterion-a"}}}},
	})
	if err == nil || !strings.Contains(err.Error(), "criterion-a") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateVerdictForEvidenceAllowsIncompleteCoverageForTerminalEscalation(t *testing.T) {
	t.Parallel()
	quality := &profile.Quality{ReviewDimensions: []profile.ReviewDimension{{ID: "criterion-a"}}}
	for _, status := range []string{"hard_stop", "needs_supervisor_review"} {
		status := status
		t.Run(status, func(t *testing.T) {
			t.Parallel()
			err := ValidateVerdictForEvidence(Verdict{
				Status: status, Summary: "external decision required", Confidence: "high", QualityCoverage: []QualityCoverage{},
			}, Evidence{Profiles: profile.Bundle{Quality: quality}})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestValidateVerdictForEvidenceRejectsMalformedCoverageForTerminalEscalation(t *testing.T) {
	t.Parallel()
	quality := &profile.Quality{ReviewDimensions: []profile.ReviewDimension{{ID: "criterion-a"}}}
	for _, status := range []string{"hard_stop", "needs_supervisor_review"} {
		status := status
		t.Run(status, func(t *testing.T) {
			t.Parallel()
			err := ValidateVerdictForEvidence(Verdict{
				Status: status, Summary: "external decision required", Confidence: "high",
				QualityCoverage: []QualityCoverage{{Criterion: "criterion-a", ChangedSurface: "file.go"}},
			}, Evidence{Profiles: profile.Bundle{Quality: quality}})
			if err == nil || !strings.Contains(err.Error(), "evidence_checked") {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestValidateQualityCoverageRejectsInvalidAuditEntries(t *testing.T) {
	t.Parallel()
	quality := &profile.Quality{ReviewDimensions: []profile.ReviewDimension{{ID: "criterion-a"}}}
	valid := QualityCoverage{Criterion: "criterion-a", ChangedSurface: "file.go", EvidenceChecked: []string{"diff lines"}}
	tests := []struct {
		name     string
		coverage []QualityCoverage
		want     string
	}{
		{name: "unknown criterion", coverage: []QualityCoverage{{Criterion: "criterion-b", ChangedSurface: "file.go", EvidenceChecked: []string{"diff"}}}, want: "not configured"},
		{name: "missing surface", coverage: []QualityCoverage{{Criterion: "criterion-a", EvidenceChecked: []string{"diff"}}}, want: "changed_surface"},
		{name: "missing evidence", coverage: []QualityCoverage{{Criterion: "criterion-a", ChangedSurface: "file.go"}}, want: "evidence_checked"},
		{name: "blank evidence", coverage: []QualityCoverage{{Criterion: "criterion-a", ChangedSurface: "file.go", EvidenceChecked: []string{" "}}}, want: "evidence_checked"},
		{name: "duplicate pair", coverage: []QualityCoverage{valid, valid}, want: "duplicate"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateQualityCoverage(tt.coverage, quality, true)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestValidateVerdictForEvidenceAcceptsCriterionReviewedAgainstFinalDiff(t *testing.T) {
	t.Parallel()
	quality := &profile.Quality{ReviewDimensions: []profile.ReviewDimension{{ID: "criterion-a"}}}
	err := ValidateVerdictForEvidence(Verdict{
		Status: "accepted", Summary: "ok", Confidence: "high", ReviewedFiles: []string{"file.go"},
		QualityCoverage: []QualityCoverage{{Criterion: "criterion-a", ChangedSurface: "final diff", EvidenceChecked: []string{"The criterion does not govern a narrower changed surface."}}},
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
		QualityCoverage: []QualityCoverage{{
			Criterion: "criterion-a", ChangedSurface: "file.go", EvidenceChecked: []string{"criterion is not satisfied"},
		}},
		Findings:   []Finding{{Severity: "low", Category: "criterion-a", Summary: "criterion is not satisfied", BlocksAcceptance: false}},
		Confidence: "high",
	}, Evidence{
		Task:      task.Task{AcceptanceCriteria: []task.AcceptanceCriterion{{ID: "AC1", Text: "done"}}},
		Profiles:  profile.Bundle{Quality: quality},
		DiffDirty: true,
	})
	if err == nil || !strings.Contains(err.Error(), "required quality criterion") {
		t.Fatalf("unexpected error: %v", err)
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
		QualityCoverage: []QualityCoverage{
			{Criterion: "criterion-a", ChangedSurface: "file.go", EvidenceChecked: []string{"criterion evidence"}},
			{Criterion: "criterion-b", ChangedSurface: "file.go", EvidenceChecked: []string{"criterion gap"}},
		},
		Findings:   []Finding{{Severity: "low", Category: "criterion-b", Summary: "criterion gap", BlocksAcceptance: false}},
		Confidence: "high",
	}, Evidence{
		Task:      task.Task{AcceptanceCriteria: []task.AcceptanceCriterion{{ID: "AC1", Text: "done"}}},
		Profiles:  profile.Bundle{Quality: quality},
		DiffDirty: true,
	})
	if err == nil || !strings.Contains(err.Error(), "quality score 25") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestQualityFindingScoreReturnsFullScoreWhenTotalWeightIsZero(t *testing.T) {
	t.Parallel()
	quality := &profile.Quality{ReviewDimensions: []profile.ReviewDimension{{ID: "criterion-a", Weight: 0}}}
	if got := qualityFindingScore([]Finding{{Category: "criterion-a"}}, quality); got != 100 {
		t.Fatalf("score = %d, want 100", got)
	}
}

func TestValidateVerdictForEvidenceRejectsAcceptedWithBlockingFinding(t *testing.T) {
	err := ValidateVerdictForEvidence(Verdict{
		Status:             "accepted",
		Summary:            "ok",
		ReviewedFiles:      []string{"file.go"},
		AcceptanceEvidence: []AcceptanceEvidence{{ACID: "AC1", Evidence: []string{"diff"}}},
		Findings:           []Finding{{Severity: "medium", Category: "correctness", Summary: "ordering bug", BlocksAcceptance: false}},
		Confidence:         "high",
		QualityCoverage:    []QualityCoverage{},
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
		Findings:           []Finding{{Severity: "low", Category: "style", Summary: "wording", BlocksAcceptance: false}},
		Confidence:         "medium",
		QualityCoverage:    []QualityCoverage{},
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
		Findings:           []Finding{{Severity: "low", Category: "style", Summary: "wording", BlocksAcceptance: false}},
		Confidence:         "medium",
		QualityCoverage:    []QualityCoverage{},
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
		Findings:           []Finding{{Severity: "low", Category: "style", Summary: "wording", BlocksAcceptance: true}},
		Confidence:         "medium",
		QualityCoverage:    []QualityCoverage{},
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
		Status:          "needs_revision",
		Summary:         "needs work",
		NextWorkOrder:   "fix the failing test",
		Findings:        []Finding{{Severity: "medium", Category: "correctness", Summary: "bug", BlocksAcceptance: false}},
		Confidence:      "medium",
		QualityCoverage: []QualityCoverage{},
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

func TestValidateVerdictForEvidenceAllowsDiscussionItemsOnlyForAccepted(t *testing.T) {
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
		QualityCoverage:    []QualityCoverage{},
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
		QualityCoverage: []QualityCoverage{},
	}
	if err := ValidateVerdictForEvidence(revision, evidence); err == nil || !strings.Contains(err.Error(), "discussion_items") {
		t.Fatalf("expected non-accepted discussion item rejection, got %v", err)
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
