package profile

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/shinpr/galley/internal/provider"
)

func TestValidateEnvironmentExecutorDefaults(t *testing.T) {
	t.Parallel()
	base := func() Environment {
		return Environment{
			ID:  "local",
			CWD: "/tmp/repo",
			Constraints: Constraints{
				Network:             "approval_required",
				SecretsPolicy:       "never_read_env_files",
				DestructiveCommands: "deny",
			},
		}
	}
	cases := []struct {
		name    string
		exec    *ExecutorDefault
		wantOK  bool
		wantErr string
	}{
		{name: "absent executor", exec: nil, wantOK: true},
		{name: "default_cli only", exec: &ExecutorDefault{DefaultCLI: "claude"}, wantOK: true},
		{name: "default_cli empty string is omission", exec: &ExecutorDefault{DefaultCLI: ""}, wantOK: true},
		{name: "model only", exec: &ExecutorDefault{Model: "provider-model"}, wantOK: true},
		{name: "complete defaults", exec: &ExecutorDefault{DefaultCLI: "codex", Model: "gpt", Effort: "minimal"}, wantOK: true},
		{name: "claude accepts max", exec: &ExecutorDefault{DefaultCLI: "claude", Effort: "max"}, wantOK: true},
		{name: "claude rejects minimal", exec: &ExecutorDefault{DefaultCLI: "claude", Effort: "minimal"}, wantErr: "executor.effort for claude must be one of"},
		{name: "effort without default_cli accepts provider union", exec: &ExecutorDefault{Effort: "minimal"}, wantOK: true},
		{name: "effort without default_cli accepts grok value", exec: &ExecutorDefault{Effort: "none"}, wantOK: true},
		{name: "effort without default_cli rejects unknown", exec: &ExecutorDefault{Effort: "turbo"}, wantErr: "executor.effort must be one of"},
		{name: "invalid default_cli", exec: &ExecutorDefault{DefaultCLI: "opus"}, wantErr: "executor.default_cli"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			env := base()
			env.Executor = tc.exec
			result := ValidateEnvironment(env)
			if tc.wantOK {
				if !result.Valid() {
					t.Fatalf("expected valid, got %#v", result.Errors)
				}
				return
			}
			if result.Valid() {
				t.Fatal("expected validation failure")
			}
			if !strings.Contains(strings.Join(result.Errors, "\n"), tc.wantErr) {
				t.Fatalf("errors %#v want substring %q", result.Errors, tc.wantErr)
			}
		})
	}
}

// defaultCLICase drives Go validation and generated environment schema
// semantics for optional executor.default_cli, including empty strings that
// YAML decode produces for both omitted and explicitly empty fields.
type defaultCLICase struct {
	name  string
	set   bool
	value string
	valid bool
}

var executorDefaultCLIMatrix = []defaultCLICase{
	{name: "absent", set: false, valid: true},
	{name: "empty", set: true, value: "", valid: true},
	{name: "provider-valid claude", set: true, value: "claude", valid: true},
	{name: "provider-valid codex", set: true, value: "codex", valid: true},
	{name: "provider-valid glm", set: true, value: "glm", valid: true},
	{name: "provider-valid grok", set: true, value: "grok", valid: true},
	{name: "invalid", set: true, value: "opus", valid: false},
}

func TestEnvironmentExecutorDefaultCLIRuntimeAndSchemaParity(t *testing.T) {
	t.Parallel()
	data, err := EnvironmentJSONSchema()
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	executor := schema["properties"].(map[string]any)["executor"].(map[string]any)
	execProps := executor["properties"].(map[string]any)
	assertDefaultCLIEnumCoversProviders(t, enumStrings(execProps["default_cli"].(map[string]any)["enum"].([]any)))

	for _, tc := range executorDefaultCLIMatrix {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			doc := map[string]any{}
			if tc.set {
				doc["default_cli"] = tc.value
			}
			schemaValid := len(validateExecutorDefaultCLIDoc(executor, doc)) == 0
			runtimeValid := runtimeAcceptsDefaultCLI(tc.set, tc.value)

			if schemaValid != runtimeValid {
				t.Fatalf("drift for %v: schema valid=%v runtime valid=%v schema_errs=%v",
					doc, schemaValid, runtimeValid, validateExecutorDefaultCLIDoc(executor, doc))
			}
			if schemaValid != tc.valid {
				t.Fatalf("case %q: got valid=%v, want %v", tc.name, schemaValid, tc.valid)
			}
		})
	}
}

// assertDefaultCLIEnumCoversProviders pins that the schema enum accepts every
// executor provider plus the empty string (default_cli omitted).
func assertDefaultCLIEnumCoversProviders(t *testing.T, cliEnum []string) {
	t.Helper()
	if !containsString(cliEnum, "") {
		t.Fatalf("executor.default_cli schema must accept empty string, got %#v", cliEnum)
	}
	for _, id := range provider.ExecutorIDs() {
		if !containsString(cliEnum, id) {
			t.Fatalf("executor.default_cli schema missing %q in %#v", id, cliEnum)
		}
	}
}

// runtimeAcceptsDefaultCLI reports whether ValidateEnvironment accepts the
// value, ignoring errors from unrelated fields.
func runtimeAcceptsDefaultCLI(set bool, value string) bool {
	env := Environment{
		ID:  "local",
		CWD: "/tmp/repo",
		Constraints: Constraints{
			Network:             "approval_required",
			SecretsPolicy:       "never_read_env_files",
			DestructiveCommands: "deny",
		},
	}
	if set {
		env.Executor = &ExecutorDefault{DefaultCLI: value}
	}
	for _, e := range ValidateEnvironment(env).Errors {
		if strings.Contains(e, "executor.default_cli") {
			return false
		}
	}
	return true
}

func validateExecutorDefaultCLIDoc(executor, doc map[string]any) []string {
	var errs []string
	props := executor["properties"].(map[string]any)
	if cli, present := doc["default_cli"]; present {
		enum := props["default_cli"].(map[string]any)["enum"].([]any)
		if !effortEnumContains(enum, cli) {
			errs = append(errs, fmt.Sprintf("default_cli %v not in enum", cli))
		}
	}
	return errs
}

func enumStrings(raw []any) []string {
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func containsString(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

func TestEnvironmentJSONSchemaExecutorDefaultsParity(t *testing.T) {
	t.Parallel()
	data, err := EnvironmentJSONSchema()
	if err != nil {
		t.Fatalf("EnvironmentJSONSchema: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	props := doc["properties"].(map[string]any)
	executor := props["executor"].(map[string]any)
	execProps := executor["properties"].(map[string]any)
	for _, key := range []string{"default_cli", "model", "effort"} {
		if _, ok := execProps[key]; !ok {
			t.Fatalf("environment executor schema missing %s", key)
		}
	}
	if _, hasPrompt := execProps["prompt_profile"]; hasPrompt {
		t.Fatal("environment executor schema must not expose prompt_profile")
	}
	if _, hasMode := execProps["prompt_mode"]; hasMode {
		t.Fatal("environment executor schema must not expose prompt_mode")
	}
	// Conditional effort enums must cover every executor provider.
	allOf, _ := executor["allOf"].([]any)
	if len(allOf) == 0 {
		t.Fatal("expected executor effort allOf conditionals")
	}
	seen := map[string]bool{}
	for _, rule := range allOf {
		condition := rule.(map[string]any)["if"].(map[string]any)
		ifProps := condition["properties"].(map[string]any)
		cli := ifProps["default_cli"].(map[string]any)["const"].(string)
		seen[cli] = true
	}
	for _, id := range provider.ExecutorIDs() {
		if !seen[id] {
			t.Fatalf("missing effort conditional for executor %q", id)
		}
	}
}
