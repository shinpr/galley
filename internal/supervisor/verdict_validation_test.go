package supervisor

import (
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/shinpr/galley/internal/task"
)

func TestValidateVerdictUsesStatusAndSummaryAsRoutingContract(t *testing.T) {
	t.Parallel()
	for _, status := range []string{"accepted", "needs_revision", "needs_supervisor_review", "hard_stop"} {
		t.Run(status, func(t *testing.T) {
			if err := ValidateVerdict(structurallyCompleteVerdict(status)); err != nil {
				t.Fatalf("ValidateVerdict() error = %v", err)
			}
		})
	}
	unknown := structurallyCompleteVerdict("unknown")
	if err := ValidateVerdict(unknown); err == nil {
		t.Fatal("unknown status was accepted")
	}
	emptySummary := structurallyCompleteVerdict("accepted")
	emptySummary.Summary = ""
	if err := ValidateVerdict(emptySummary); err == nil {
		t.Fatal("empty summary was accepted")
	}
	incomplete := structurallyCompleteVerdict("accepted")
	incomplete.Findings = nil
	if err := ValidateVerdict(incomplete); err == nil {
		t.Fatal("missing findings array was accepted")
	}
}

func structurallyCompleteVerdict(status string) Verdict {
	return Verdict{
		Status:           status,
		Summary:          "review result",
		AcceptancePasses: []string{},
		QualityPasses:    []string{},
		Findings:         []string{},
		DiscussionItems:  []string{},
	}
}

func TestValidateVerdictForEvidenceDoesNotRejectIncompletePassLists(t *testing.T) {
	t.Parallel()
	verdict := Verdict{
		Status:           "accepted",
		Summary:          "ready for human review",
		AcceptancePasses: []string{"AC1"},
		QualityPasses:    []string{"regression"},
		Findings:         []string{"AC2 remains a visible review gap."},
		DiscussionItems:  []string{},
	}
	if err := ValidateVerdictForEvidence(verdict, Evidence{}); err != nil {
		t.Fatalf("accepted verdict was rejected: %v", err)
	}
}

func TestVerdictJSONContainsOnlyRuntimeInputs(t *testing.T) {
	t.Parallel()
	data, err := json.Marshal(Verdict{
		Status:           "accepted",
		Summary:          "reviewed",
		AcceptancePasses: []string{"AC1"},
		QualityPasses:    []string{"regression"},
		Findings:         []string{},
		DiscussionItems:  []string{"Check the documented tradeoff."},
	})
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(decoded))
	for key := range decoded {
		got = append(got, key)
	}
	sort.Strings(got)
	want := []string{"acceptance_passes", "discussion_items", "findings", "quality_passes", "status", "summary"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("verdict JSON keys = %v; want %v", got, want)
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
		PreflightResult: map[string]any{"status": "completed", "outputs": []map[string]any{{"ac_id": "AC1"}}},
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
		`"preflight_result":{"outputs":[{"ac_id":"AC1"}],"status":"completed"}`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("request JSON missing %s: %s", want, text)
		}
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
