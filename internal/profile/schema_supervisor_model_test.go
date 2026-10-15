package profile

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
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
	if _, exists := effort["minLength"]; exists {
		t.Fatalf("supervisor.effort must allow the empty CLI-default value: %#v", effort)
	}

	allOf, ok := supervisor["allOf"].([]any)
	if !ok || len(allOf) != 5 {
		t.Fatalf("supervisor allOf = %#v, want 5 conditional effort rules", supervisor["allOf"])
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
	if !reflect.DeepEqual(got["kimi"], wantClaude) {
		t.Fatalf("supervisor kimi effort enum = %#v, want %#v", got["kimi"], wantClaude)
	}
	wantGrok := []string{"", "none", "minimal", "low", "medium", "high", "xhigh", "max"}
	if !reflect.DeepEqual(got["grok"], wantGrok) {
		t.Fatalf("supervisor grok effort enum = %#v, want %#v", got["grok"], wantGrok)
	}
}

type supervisorEffortCase struct {
	name       string
	defaultCLI string
	effortSet  bool
	effort     string
	wantErr    string // empty means valid
}

// One matrix drives both runtime and schema validation, including omitted fields.
var supervisorEffortMatrix = []supervisorEffortCase{
	{"claude explicit", "claude", true, "high", ""},
	{"claude omitted", "claude", false, "", ""},
	{"claude empty", "claude", true, "", ""},
	{"claude invalid codex-only value", "claude", true, "minimal", "supervisor.effort for claude must be one of"},
	{"codex explicit", "codex", true, "minimal", ""},
	{"codex omitted", "codex", false, "", ""},
	{"codex empty", "codex", true, "", ""},
	{"codex invalid unknown value", "codex", true, "turbo", "supervisor.effort for codex must be one of"},
	{"glm explicit", "glm", true, "xhigh", ""},
	{"glm omitted", "glm", false, "", ""},
	{"glm empty", "glm", true, "", ""},
	{"glm invalid codex-only value", "glm", true, "minimal", "supervisor.effort for glm must be one of"},
	{"default_cli omitted union value", "", true, "minimal", ""},
	{"default_cli omitted claude value", "", true, "max", ""},
	{"default_cli omitted empty", "", true, "", ""},
	{"default_cli omitted absent", "", false, "", ""},
	{"default_cli omitted unknown value", "", true, "turbo", "supervisor.effort must be one of"},
}

func (c supervisorEffortCase) supervisorDoc() map[string]any {
	doc := map[string]any{}
	if c.defaultCLI != "" {
		doc["default_cli"] = c.defaultCLI
	}
	if c.effortSet {
		doc["effort"] = c.effort
	}
	return doc
}

func TestSupervisorEffortRuntimeAndSchemaParity(t *testing.T) {
	data, err := EnvironmentJSONSchema()
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	supervisor := schema["properties"].(map[string]any)["supervisor"].(map[string]any)

	for _, tc := range supervisorEffortMatrix {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			schemaValid := len(validateSupervisorEffortDoc(supervisor, tc.supervisorDoc())) == 0
			runtimeErrs := runtimeSupervisorEffortErrors(tc)
			runtimeValid := len(runtimeErrs) == 0
			if schemaValid != runtimeValid {
				t.Fatalf("drift for %v: schema valid=%v, runtime valid=%v", tc.supervisorDoc(), schemaValid, runtimeValid)
			}
			if wantValid := tc.wantErr == ""; schemaValid != wantValid {
				t.Fatalf("case %q: got valid=%v, want %v", tc.name, schemaValid, wantValid)
			}
			if !strings.Contains(strings.Join(runtimeErrs, "\n"), tc.wantErr) {
				t.Fatalf("runtime errors %#v want substring %q", runtimeErrs, tc.wantErr)
			}
		})
	}
}

func runtimeSupervisorEffortErrors(c supervisorEffortCase) []string {
	env := Environment{
		ID:  "env",
		CWD: "/repo",
		Constraints: Constraints{
			Network:             "approval_required",
			SecretsPolicy:       "never_read_env_files",
			DestructiveCommands: "deny",
		},
		Supervisor: &SupervisorDefault{DefaultCLI: c.defaultCLI},
	}
	if c.effortSet {
		env.Supervisor.Effort = c.effort
	}
	var errs []string
	for _, e := range ValidateEnvironment(env).Errors {
		if strings.Contains(e, "effort") {
			errs = append(errs, e)
		}
	}
	return errs
}

func validateSupervisorEffortDoc(supervisor, doc map[string]any) []string {
	var errs []string
	effort, present := doc["effort"]
	if present {
		baseEnum := supervisor["properties"].(map[string]any)["effort"].(map[string]any)["enum"].([]any)
		if !effortEnumContains(baseEnum, effort) {
			errs = append(errs, fmt.Sprintf("effort %v not in base enum", effort))
		}
	}
	allOf, _ := supervisor["allOf"].([]any)
	for _, raw := range allOf {
		rule := raw.(map[string]any)
		ifProps := rule["if"].(map[string]any)["properties"].(map[string]any)
		cli := ifProps["default_cli"].(map[string]any)["const"].(string)
		if dc, ok := doc["default_cli"].(string); !ok || dc != cli {
			continue
		}
		if !present {
			continue
		}
		enum := rule["then"].(map[string]any)["properties"].(map[string]any)["effort"].(map[string]any)["enum"].([]any)
		if !effortEnumContains(enum, effort) {
			errs = append(errs, fmt.Sprintf("effort %v not accepted for default_cli %q", effort, cli))
		}
	}
	return errs
}

func effortEnumContains(enum []any, value any) bool {
	for _, allowed := range enum {
		if allowed == value {
			return true
		}
	}
	return false
}
