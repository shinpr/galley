package profile

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/shinpr/galley/internal/provider"
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
	// Base property enum is the empty CLI-default value plus the provider-level
	// supervisor effort union so a value accepted by no supervisor provider is
	// rejected even when default_cli is omitted, matching the runtime union check.
	baseEnum, ok := effort["enum"].([]any)
	if !ok {
		t.Fatalf("supervisor.effort base must fix an enum: %#v", effort)
	}
	var gotBase []string
	for _, v := range baseEnum {
		gotBase = append(gotBase, v.(string))
	}
	wantBase := append([]string{""}, provider.SupervisorEfforts()...)
	if !reflect.DeepEqual(gotBase, wantBase) {
		t.Fatalf("supervisor.effort base enum = %#v, want %#v", gotBase, wantBase)
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

// supervisorEffortCase is one supervisor.effort input in the shared matrix that
// TestEnvironmentSchemaSupervisorEffortSemantics and
// TestSupervisorEffortRuntimeAndSchemaParity both drive. effortSet distinguishes
// an omitted effort from an explicitly empty effort, and defaultCLI == "" models
// an omitted supervisor.default_cli.
type supervisorEffortCase struct {
	name       string
	defaultCLI string
	effortSet  bool
	effort     string
	valid      bool
}

// supervisorEffortMatrix is the single accepted/rejected matrix compared across
// runtime validation (ValidateEnvironment) and the generated JSON Schema. It
// covers each supervisor default_cli plus the default_cli-omitted branch, where
// the effective supervisor is resolved later so the provider-level union governs:
// a union-valid value is accepted, an omitted or empty effort is accepted, and an
// unknown value is rejected.
var supervisorEffortMatrix = []supervisorEffortCase{
	{"claude explicit", "claude", true, "high", true},
	{"claude omitted", "claude", false, "", true},
	{"claude empty", "claude", true, "", true},
	{"claude invalid codex-only value", "claude", true, "minimal", false},
	{"codex explicit", "codex", true, "minimal", true},
	{"codex omitted", "codex", false, "", true},
	{"codex empty", "codex", true, "", true},
	{"codex invalid unknown value", "codex", true, "turbo", false},
	{"glm explicit", "glm", true, "xhigh", true},
	{"glm omitted", "glm", false, "", true},
	{"glm empty", "glm", true, "", true},
	{"glm invalid codex-only value", "glm", true, "minimal", false},
	{"default_cli omitted union value", "", true, "minimal", true},
	{"default_cli omitted claude value", "", true, "max", true},
	{"default_cli omitted empty", "", true, "", true},
	{"default_cli omitted absent", "", false, "", true},
	{"default_cli omitted unknown value", "", true, "turbo", false},
}

// supervisorDoc renders a matrix case as a supervisor JSON Schema document,
// omitting default_cli and effort when the case leaves them unset.
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

// TestEnvironmentSchemaSupervisorEffortSemantics drives the generated schema's
// base effort enum and conditional effort rules against the shared matrix. It
// proves, per selected default_cli and for the default_cli-omitted branch, that a
// provider-valid effort, an omitted effort, and an explicitly empty effort
// validate, while an effort outside the accepted set is rejected. The accepted
// values come from the generated schema, so this fails if the schema stops
// permitting the empty CLI-default value or the provider-level union.
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

	for _, tc := range supervisorEffortMatrix {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			errs := validateSupervisorEffortDoc(supervisor, tc.supervisorDoc())
			if tc.valid && len(errs) > 0 {
				t.Fatalf("doc %v should validate, got %v", tc.supervisorDoc(), errs)
			}
			if !tc.valid && len(errs) == 0 {
				t.Fatalf("doc %v should be rejected by the effort schema", tc.supervisorDoc())
			}
		})
	}
}

// TestSupervisorEffortRuntimeAndSchemaParity compares the same accepted/rejected
// matrix across the two executable representations of the supervisor.effort
// contract: runtime ValidateEnvironment and the generated JSON Schema. A schema
// that accepts a value the runtime rejects (or vice versa) fails here, which is
// the exact drift the contract-sync dimension guards. It intentionally exercises
// the default_cli-omitted branch, where the earlier schema left the base effort
// unconstrained while the runtime enforced the provider-level union.
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
			runtimeValid := runtimeSupervisorEffortValid(tc)
			if schemaValid != runtimeValid {
				t.Fatalf("drift for %v: schema valid=%v, runtime valid=%v", tc.supervisorDoc(), schemaValid, runtimeValid)
			}
			if schemaValid != tc.valid {
				t.Fatalf("case %q: got valid=%v, want %v", tc.name, schemaValid, tc.valid)
			}
		})
	}
}

// runtimeSupervisorEffortValid reports whether ValidateEnvironment accepts the
// case's supervisor.effort. It builds an otherwise-valid environment so any
// remaining error is effort-scoped, then inspects only effort errors.
func runtimeSupervisorEffortValid(c supervisorEffortCase) bool {
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
	for _, e := range ValidateEnvironment(env).Errors {
		if strings.Contains(e, "effort") {
			return false
		}
	}
	return true
}

// validateSupervisorEffortDoc evaluates the supervisor block against a document
// using the JSON Schema semantics its effort rules encode: a present effort must
// be in the base property enum (which applies regardless of default_cli), and,
// when a conditional rule's if-const matches the document's default_cli, the
// present effort must also be in that rule's then-enum. An omitted effort is
// vacuously valid. It reads the enums straight from the generated schema rather
// than hardcoding them, so the semantics track schema changes.
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
