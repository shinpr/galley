package supervisor

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/shinpr/galley/internal/profile"
	"github.com/shinpr/galley/internal/task"
)

func TestValidateVerdictRejectsNeedsRevisionWithoutWorkOrder(t *testing.T) {
	err := ValidateVerdict(Verdict{Status: "needs_revision", Summary: "gaps", Confidence: "high"})
	if err == nil {
		t.Fatal("expected validation error")
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
	}
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	got := append([]string(nil), schema.Properties["status"].Enum...)
	want := []string{"accepted", "hard_stop", "needs_revision", "needs_supervisor_review"}
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("schema status enum got %#v, want %#v", got, want)
	}
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

func TestValidateVerdictForEvidenceRejectsAcceptedWithoutRepositoryReview(t *testing.T) {
	err := ValidateVerdictForEvidence(Verdict{
		Status:             "accepted",
		Summary:            "ok",
		AcceptanceEvidence: []AcceptanceEvidence{{ACID: "AC1", Evidence: []string{"diff"}}},
		Confidence:         "high",
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
		Status:        "accepted",
		Summary:       "ok",
		ReviewedFiles: []string{"file.go"},
		Confidence:    "high",
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

func TestValidateVerdictForEvidenceRejectsAcceptedWithBlockingFinding(t *testing.T) {
	err := ValidateVerdictForEvidence(Verdict{
		Status:             "accepted",
		Summary:            "ok",
		ReviewedFiles:      []string{"file.go"},
		AcceptanceEvidence: []AcceptanceEvidence{{ACID: "AC1", Evidence: []string{"diff"}}},
		Findings:           []Finding{{Severity: "medium", Category: "correctness", Summary: "ordering bug", BlocksAcceptance: false}},
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
		Findings:           []Finding{{Severity: "low", Category: "style", Summary: "wording", BlocksAcceptance: false}},
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
		Findings:           []Finding{{Severity: "low", Category: "style", Summary: "wording", BlocksAcceptance: false}},
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
		Findings:           []Finding{{Severity: "low", Category: "style", Summary: "wording", BlocksAcceptance: true}},
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
