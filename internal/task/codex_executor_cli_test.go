package task

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestExecutorCLISchemaEnumIncludesClaudeAndCodex(t *testing.T) {
	t.Parallel()
	got := ExecutorCLIEnum()
	want := []string{"claude", "codex", "glm", "grok", "kimi"}
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
	// Empty is included so explicit `cli: ""` matches Go structural validation
	// (YAML zero values) and remains an independent optional override.
	wantEnum := append([]string{""}, want...)
	if len(rawEnum) != len(wantEnum) {
		t.Fatalf("expected %d-value enum, got %#v", len(wantEnum), rawEnum)
	}
	gotEnum := []string{}
	for _, v := range rawEnum {
		s, _ := v.(string)
		gotEnum = append(gotEnum, s)
	}
	if !reflect.DeepEqual(gotEnum, wantEnum) {
		t.Fatalf("generated executor.cli enum = %#v, want %#v", gotEnum, wantEnum)
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
	if len(allOf) != 5 {
		t.Fatalf("executor schema allOf = %#v, want 5 conditional effort rules", allOf)
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

	// Empty is included so authors can omit effort under a selected cli.
	if !reflect.DeepEqual(got["claude"], []string{"", "low", "medium", "high", "xhigh", "max"}) {
		t.Fatalf("claude effort enum drift: %#v", got["claude"])
	}
	if !reflect.DeepEqual(got["codex"], []string{"", "minimal", "low", "medium", "high", "xhigh", "max"}) {
		t.Fatalf("codex effort enum drift: %#v", got["codex"])
	}
	if !reflect.DeepEqual(got["glm"], []string{"", "low", "medium", "high", "xhigh", "max"}) {
		t.Fatalf("glm effort enum drift: %#v", got["glm"])
	}
	if !reflect.DeepEqual(got["grok"], []string{"", "none", "minimal", "low", "medium", "high", "xhigh", "max"}) {
		t.Fatalf("grok effort enum drift: %#v", got["grok"])
	}
	if !reflect.DeepEqual(got["kimi"], []string{"", "low", "medium", "high", "xhigh", "max"}) {
		t.Fatalf("kimi effort enum drift: %#v", got["kimi"])
	}
	// Retired prompt transport fields must not reappear on the task contract.
	execProps, _ := exec["properties"].(map[string]any)
	if _, ok := execProps["prompt_profile"]; ok {
		t.Fatal("task schema must not expose executor.prompt_profile")
	}
	if _, ok := execProps["prompt_mode"]; ok {
		t.Fatal("task schema must not expose executor.prompt_mode")
	}
	if required, _ := exec["required"].([]any); len(required) != 0 {
		t.Fatalf("executor fields must be independently optional, required=%#v", required)
	}
}
