package task

// AC: AC1 — Task validation and generated schemas accept executor.cli "codex"
// while preserving backward compatibility for existing Claude task files.
//
// Behavior under test:
//   - Trigger: a task with executor.cli="codex" is passed to the validator and
//     to schema generation/check.
//   - Process: validateExecutor (internal/task/validate.go) and the executor
//     schema enumeration (internal/task/schema.go) accept both "claude" and
//     "codex" while rejecting unknown values.
//   - Observable result: ValidationResult.OK==true for both supported CLIs;
//     the generated executor schema (under schemas/) lists both values in the
//     "cli" enum; an unknown CLI ("opus-cli") still fails with a clear error.

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestExecutorCLIAcceptsClaudeAndCodex(t *testing.T) {
	t.Parallel()
	for _, cli := range []string{"claude", "codex"} {
		cli := cli
		t.Run(cli, func(t *testing.T) {
			base := validTask(t)
			base.Executor.CLI = cli
			result := ValidateStructural(base)
			if !result.Valid() {
				t.Fatalf("expected executor.cli=%q to validate, got errors: %#v", cli, result.Errors)
			}
		})
	}
}

func TestExecutorCLIRejectsUnknownValue(t *testing.T) {
	t.Parallel()
	base := validTask(t)
	base.Executor.CLI = "opus-cli"
	result := ValidateStructural(base)
	if result.Valid() {
		t.Fatal("expected unknown executor.cli to fail validation")
	}
	combined := strings.Join(result.Errors, "\n")
	if !strings.Contains(combined, "executor.cli") {
		t.Fatalf("expected error to mention executor.cli, got %q", combined)
	}
	if !strings.Contains(combined, "claude") || !strings.Contains(combined, "codex") {
		t.Fatalf("expected error to mention supported set, got %q", combined)
	}
}

func TestExecutorEffortValidationDependsOnCLI(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		cli       string
		effort    string
		wantValid bool
		wantError string
	}{
		{name: "claude accepts max", cli: "claude", effort: "max", wantValid: true},
		{name: "claude accepts xhigh", cli: "claude", effort: "xhigh", wantValid: true},
		{name: "codex accepts high", cli: "codex", effort: "high", wantValid: true},
		{name: "codex rejects xhigh", cli: "codex", effort: "xhigh", wantError: "executor.effort for codex must be one of"},
		{name: "claude rejects unknown", cli: "claude", effort: "turbo", wantError: "executor.effort for claude must be one of"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			base := validTask(t)
			base.Executor.CLI = tc.cli
			base.Executor.Effort = tc.effort

			result := ValidateStructural(base)
			if tc.wantValid {
				if !result.Valid() {
					t.Fatalf("expected valid effort, got errors: %#v", result.Errors)
				}
				return
			}
			if result.Valid() {
				t.Fatal("expected invalid effort")
			}
			combined := strings.Join(result.Errors, "\n")
			if !strings.Contains(combined, tc.wantError) {
				t.Fatalf("expected error containing %q, got %q", tc.wantError, combined)
			}
		})
	}
}

func TestExecutorCLISchemaEnumIncludesClaudeAndCodex(t *testing.T) {
	t.Parallel()
	got := ExecutorCLIEnum()
	want := []string{"claude", "codex"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ExecutorCLIEnum() = %#v, want %#v", got, want)
	}

	data, err := TaskJSONSchema()
	if err != nil {
		t.Fatalf("TaskJSONSchema: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("decode schema: %v", err)
	}
	props, _ := doc["properties"].(map[string]any)
	exec, _ := props["executor"].(map[string]any)
	execProps, _ := exec["properties"].(map[string]any)
	cliNode, _ := execProps["cli"].(map[string]any)
	rawEnum, _ := cliNode["enum"].([]any)
	if len(rawEnum) != 2 {
		t.Fatalf("expected 2-value enum, got %#v", rawEnum)
	}
	gotEnum := []string{}
	for _, v := range rawEnum {
		s, _ := v.(string)
		gotEnum = append(gotEnum, s)
	}
	if !reflect.DeepEqual(gotEnum, want) {
		t.Fatalf("generated executor.cli enum = %#v, want %#v", gotEnum, want)
	}
}

func TestExecutorEffortSchemaConditionDependsOnCLI(t *testing.T) {
	t.Parallel()
	data, err := TaskJSONSchema()
	if err != nil {
		t.Fatalf("TaskJSONSchema: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("decode schema: %v", err)
	}
	props, _ := doc["properties"].(map[string]any)
	exec, _ := props["executor"].(map[string]any)
	allOf, _ := exec["allOf"].([]any)
	if len(allOf) != 2 {
		t.Fatalf("executor schema allOf = %#v, want 2 conditional effort rules", allOf)
	}

	got := map[string][]string{}
	for _, raw := range allOf {
		rule, _ := raw.(map[string]any)
		ifNode, _ := rule["if"].(map[string]any)
		ifProps, _ := ifNode["properties"].(map[string]any)
		cliNode, _ := ifProps["cli"].(map[string]any)
		cli, _ := cliNode["const"].(string)
		thenNode, _ := rule["then"].(map[string]any)
		thenProps, _ := thenNode["properties"].(map[string]any)
		effortNode, _ := thenProps["effort"].(map[string]any)
		rawEnum, _ := effortNode["enum"].([]any)
		for _, value := range rawEnum {
			if s, ok := value.(string); ok {
				got[cli] = append(got[cli], s)
			}
		}
	}

	if !reflect.DeepEqual(got["claude"], []string{"low", "medium", "high", "xhigh", "max"}) {
		t.Fatalf("claude effort enum drift: %#v", got["claude"])
	}
	if !reflect.DeepEqual(got["codex"], []string{"low", "medium", "high"}) {
		t.Fatalf("codex effort enum drift: %#v", got["codex"])
	}
}
