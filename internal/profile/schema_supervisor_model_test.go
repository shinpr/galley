package profile

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
)

func TestEnvironmentSchemaSupervisorModelContract(t *testing.T) {
	data, err := EnvironmentJSONSchema()
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}

	properties := schema["properties"].(map[string]any)
	supervisor := properties["supervisor"].(map[string]any)
	model := supervisor["properties"].(map[string]any)["model"].(map[string]any)
	if model["type"] != "string" {
		t.Fatalf("supervisor.model type got %#v, want string", model["type"])
	}
	if _, exists := model["minLength"]; exists {
		t.Fatalf("supervisor.model must allow the empty CLI-default value: %#v", model)
	}
}

func TestEnvironmentSchemaSupervisorEffortContract(t *testing.T) {
	data, err := EnvironmentJSONSchema()
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}

	supervisor := schema["properties"].(map[string]any)["supervisor"].(map[string]any)
	effort := supervisor["properties"].(map[string]any)["effort"].(map[string]any)
	if effort["type"] != "string" {
		t.Fatalf("supervisor.effort type got %#v, want string", effort["type"])
	}
	// Base property carries no enum/minLength so an omitted or empty value stays
	// valid; the accepted set is applied conditionally per default_cli.
	if _, exists := effort["enum"]; exists {
		t.Fatalf("supervisor.effort base must not fix an enum: %#v", effort)
	}
	if _, exists := effort["minLength"]; exists {
		t.Fatalf("supervisor.effort must allow the empty CLI-default value: %#v", effort)
	}

	allOf, ok := supervisor["allOf"].([]any)
	if !ok || len(allOf) != 3 {
		t.Fatalf("supervisor allOf = %#v, want 3 conditional effort rules", supervisor["allOf"])
	}
	got := map[string][]string{}
	for _, raw := range allOf {
		rule := raw.(map[string]any)
		cli := rule["if"].(map[string]any)["properties"].(map[string]any)["default_cli"].(map[string]any)["const"].(string)
		thenEffort := rule["then"].(map[string]any)["properties"].(map[string]any)["effort"].(map[string]any)
		for _, v := range thenEffort["enum"].([]any) {
			got[cli] = append(got[cli], v.(string))
		}
	}
	// Each conditional enum leads with the empty CLI-default value so an
	// explicitly empty effort stays valid, followed by the provider's set.
	wantClaude := []string{"", "low", "medium", "high", "xhigh", "max"}
	wantCodex := []string{"", "minimal", "low", "medium", "high", "xhigh", "max"}
	if !reflect.DeepEqual(got["claude"], wantClaude) {
		t.Fatalf("supervisor claude effort enum = %#v, want %#v", got["claude"], wantClaude)
	}
	if !reflect.DeepEqual(got["codex"], wantCodex) {
		t.Fatalf("supervisor codex effort enum = %#v, want %#v", got["codex"], wantCodex)
	}
	if !reflect.DeepEqual(got["glm"], wantClaude) {
		t.Fatalf("supervisor glm effort enum = %#v, want %#v", got["glm"], wantClaude)
	}
}

// TestEnvironmentSchemaSupervisorEffortSemantics drives the generated schema's
// conditional effort rules against representative supervisor documents. It
// proves, per selected default_cli, that an explicit provider-valid effort, an
// omitted effort, and an explicitly empty effort all validate, while an effort
// outside the provider's set (including a value valid only for a different
// provider) is rejected. The accepted values come from the generated schema, so
// this fails if the schema stops permitting the empty CLI-default value.
func TestEnvironmentSchemaSupervisorEffortSemantics(t *testing.T) {
	data, err := EnvironmentJSONSchema()
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	supervisor := schema["properties"].(map[string]any)["supervisor"].(map[string]any)

	cases := []struct {
		name  string
		doc   map[string]any
		valid bool
	}{
		{"claude explicit", map[string]any{"default_cli": "claude", "effort": "high"}, true},
		{"claude omitted", map[string]any{"default_cli": "claude"}, true},
		{"claude empty", map[string]any{"default_cli": "claude", "effort": ""}, true},
		{"claude invalid codex-only value", map[string]any{"default_cli": "claude", "effort": "minimal"}, false},
		{"codex explicit", map[string]any{"default_cli": "codex", "effort": "minimal"}, true},
		{"codex omitted", map[string]any{"default_cli": "codex"}, true},
		{"codex empty", map[string]any{"default_cli": "codex", "effort": ""}, true},
		{"codex invalid unknown value", map[string]any{"default_cli": "codex", "effort": "turbo"}, false},
		{"glm explicit", map[string]any{"default_cli": "glm", "effort": "xhigh"}, true},
		{"glm omitted", map[string]any{"default_cli": "glm"}, true},
		{"glm empty", map[string]any{"default_cli": "glm", "effort": ""}, true},
		{"glm invalid codex-only value", map[string]any{"default_cli": "glm", "effort": "minimal"}, false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			errs := validateSupervisorEffortDoc(supervisor, tc.doc)
			if tc.valid && len(errs) > 0 {
				t.Fatalf("doc %v should validate, got %v", tc.doc, errs)
			}
			if !tc.valid && len(errs) == 0 {
				t.Fatalf("doc %v should be rejected by the conditional effort schema", tc.doc)
			}
		})
	}
}

// validateSupervisorEffortDoc evaluates the supervisor block's allOf/if/then
// conditional effort rules against a document, applying the JSON Schema
// semantics those rules use: when a rule's if-const matches the document's
// default_cli, a present effort must be in the then-enum, and an omitted effort
// is vacuously valid. It reads the enums straight from the generated schema
// rather than hardcoding them, so the semantics track schema changes.
func validateSupervisorEffortDoc(supervisor, doc map[string]any) []string {
	var errs []string
	allOf, _ := supervisor["allOf"].([]any)
	for _, raw := range allOf {
		rule := raw.(map[string]any)
		ifProps := rule["if"].(map[string]any)["properties"].(map[string]any)
		cli := ifProps["default_cli"].(map[string]any)["const"].(string)
		if dc, ok := doc["default_cli"].(string); !ok || dc != cli {
			continue
		}
		effort, present := doc["effort"]
		if !present {
			continue
		}
		enum := rule["then"].(map[string]any)["properties"].(map[string]any)["effort"].(map[string]any)["enum"].([]any)
		matched := false
		for _, allowed := range enum {
			if allowed == effort {
				matched = true
				break
			}
		}
		if !matched {
			errs = append(errs, fmt.Sprintf("effort %v not accepted for default_cli %q", effort, cli))
		}
	}
	return errs
}
