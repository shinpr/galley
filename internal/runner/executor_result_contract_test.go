package runner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestExecutorResultSerializedShapeCompatibility(t *testing.T) {
	t.Parallel()
	data, err := json.Marshal(ExecutorResult{
		Status: "completed", Summary: "done", FilesModified: []string{},
		AcceptanceCriteria: []ExecutorAcceptanceCriterion{}, Verification: []ExecutorVerification{},
		ScopeExpansions: []ExecutorScopeExpansion{}, Decisions: []ExecutorDecision{}, Risks: []ExecutorRisk{},
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
	want := []string{"acceptance_criteria", "decisions", "files_modified", "risks", "scope_expansions", "status", "summary", "verification"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("serialized executor-result keys = %v; want %v", got, want)
	}
}

func TestExecutorResultValidationUsesProviderNeutralVocabulary(t *testing.T) {
	t.Parallel()
	err := (ExecutorResult{}).Validate()
	if err == nil || !strings.Contains(err.Error(), "executor result") || strings.Contains(err.Error(), "Claude") {
		t.Fatalf("validation error = %v", err)
	}
}

func TestExecutorResultSchemaLeavesQualityJudgmentToSupervisor(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(filepath.Join("..", "..", "schemas", "claude-result.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"quality_passes", "quality_gaps"} {
		if _, ok := schema.Properties[field]; ok {
			t.Fatalf("executor schema must not expose supervisor field %q", field)
		}
	}
}
