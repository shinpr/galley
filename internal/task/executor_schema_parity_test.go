package task

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/shinpr/galley/internal/provider"
)

// executorFieldCase drives Go structural validation and generated schema
// semantics for optional executor values, including empty strings that YAML
// decode produces for both omitted and explicitly empty fields.
type executorFieldCase struct {
	name      string
	cliSet    bool
	cli       string
	modelSet  bool
	model     string
	effortSet bool
	effort    string
	valid     bool
}

var executorOptionalFieldMatrix = []executorFieldCase{
	// Omitted block / empty fields: resolution path remains open.
	{name: "all omitted", valid: true},
	{name: "cli empty", cliSet: true, cli: "", valid: true},
	{name: "model empty", modelSet: true, model: "", valid: true},
	{name: "effort empty", effortSet: true, effort: "", valid: true},
	{name: "all empty", cliSet: true, cli: "", modelSet: true, model: "", effortSet: true, effort: "", valid: true},

	// Partial and complete pins.
	{name: "cli only", cliSet: true, cli: "claude", valid: true},
	{name: "model only", modelSet: true, model: "provider-model", valid: true},
	{name: "effort only union", effortSet: true, effort: "minimal", valid: true},
	{name: "complete", cliSet: true, cli: "codex", modelSet: true, model: "gpt", effortSet: true, effort: "minimal", valid: true},

	// Provider-conditioned effort and invalid values.
	{name: "claude accepts max", cliSet: true, cli: "claude", effortSet: true, effort: "max", valid: true},
	{name: "claude rejects minimal", cliSet: true, cli: "claude", effortSet: true, effort: "minimal", valid: false},
	{name: "invalid cli", cliSet: true, cli: "opus", valid: false},
	{name: "effort only unknown", effortSet: true, effort: "turbo", valid: false},
	{name: "codex empty effort under cli", cliSet: true, cli: "codex", effortSet: true, effort: "", valid: true},
}

func (c executorFieldCase) executorDoc() map[string]any {
	doc := map[string]any{}
	if c.cliSet {
		doc["cli"] = c.cli
	}
	if c.modelSet {
		doc["model"] = c.model
	}
	if c.effortSet {
		doc["effort"] = c.effort
	}
	return doc
}

func (c executorFieldCase) taskExecutor() Executor {
	var exec Executor
	if c.cliSet {
		exec.CLI = c.cli
	}
	if c.modelSet {
		exec.Model = c.model
	}
	if c.effortSet {
		exec.Effort = c.effort
	}
	return exec
}

func TestTaskExecutorOptionalFieldRuntimeAndSchemaParity(t *testing.T) {
	t.Parallel()
	data, err := TaskJSONSchema()
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	executor := schema["properties"].(map[string]any)["executor"].(map[string]any)

	// Schema shape: empty optional values are accepted, not only non-empty enums.
	execProps := executor["properties"].(map[string]any)
	cliEnum := enumStrings(execProps["cli"].(map[string]any)["enum"].([]any))
	if !containsString(cliEnum, "") {
		t.Fatalf("executor.cli schema must accept empty string, got %#v", cliEnum)
	}
	for _, id := range provider.ExecutorIDs() {
		if !containsString(cliEnum, id) {
			t.Fatalf("executor.cli schema missing %q in %#v", id, cliEnum)
		}
	}
	modelNode := execProps["model"].(map[string]any)
	if _, hasMin := modelNode["minLength"]; hasMin {
		t.Fatalf("executor.model must allow empty (no minLength), got %#v", modelNode)
	}
	effortEnum := enumStrings(execProps["effort"].(map[string]any)["enum"].([]any))
	if !containsString(effortEnum, "") {
		t.Fatalf("executor.effort schema must accept empty string, got %#v", effortEnum)
	}

	for _, tc := range executorOptionalFieldMatrix {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			schemaValid := len(validateTaskExecutorDoc(executor, tc.executorDoc())) == 0
			runtimeValid := runtimeTaskExecutorValid(tc)
			if schemaValid != runtimeValid {
				t.Fatalf("drift for %v: schema valid=%v runtime valid=%v schema_errs=%v",
					tc.executorDoc(), schemaValid, runtimeValid, validateTaskExecutorDoc(executor, tc.executorDoc()))
			}
			if schemaValid != tc.valid {
				t.Fatalf("case %q: got valid=%v, want %v", tc.name, schemaValid, tc.valid)
			}
		})
	}
}

func runtimeTaskExecutorValid(c executorFieldCase) bool {
	// Use ValidateStructural on a structurally complete task with the case executor.
	base := Task{
		ID:     "task-executor-parity",
		Mode:   "afk",
		Status: "draft",
		Goal:   "parity",
		AcceptanceCriteria: []AcceptanceCriterion{{
			ID: "AC1", Text: "t", Verification: "v", Status: "pending",
		}},
		Scope: Scope{
			CWD:          "/tmp/repo",
			AllowedPaths: []string{"."},
			Permission:   "edit",
		},
		ExecutionPolicy: ExecutionPolicy{
			LoopBudget:        LoopBudget{Count: 1, Set: true},
			TimeoutMS:         1000,
			AFKDecisionPolicy: "choose-smallest-reversible",
		},
		Worktree: Worktree{
			Enabled: true,
			Branch:  "agent/parity",
			Path:    "../worktrees/parity",
		},
		Executor: c.taskExecutor(),
	}
	for _, e := range ValidateStructural(base).Errors {
		if strings.Contains(e, "executor.") {
			return false
		}
	}
	return true
}

func validateTaskExecutorDoc(executor, doc map[string]any) []string {
	var errs []string
	props := executor["properties"].(map[string]any)

	if cli, present := doc["cli"]; present {
		if !effortEnumContains(props["cli"].(map[string]any)["enum"].([]any), cli) {
			errs = append(errs, fmt.Sprintf("cli %v not in enum", cli))
		}
	}
	if model, present := doc["model"]; present {
		// Model is free-form string; empty is allowed. Only type is constrained.
		if _, ok := model.(string); !ok {
			errs = append(errs, fmt.Sprintf("model %v is not a string", model))
		}
		if min, ok := props["model"].(map[string]any)["minLength"].(float64); ok {
			if s, _ := model.(string); len(s) < int(min) {
				errs = append(errs, fmt.Sprintf("model %q shorter than minLength %v", model, min))
			}
		}
	}
	if effort, present := doc["effort"]; present {
		if !effortEnumContains(props["effort"].(map[string]any)["enum"].([]any), effort) {
			errs = append(errs, fmt.Sprintf("effort %v not in base enum", effort))
		}
		// Apply provider-conditioned allOf when cli is present (including empty).
		cli, cliPresent := doc["cli"].(string)
		if cliPresent && cli != "" {
			for _, raw := range executor["allOf"].([]any) {
				rule := raw.(map[string]any)
				ifConst := rule["if"].(map[string]any)["properties"].(map[string]any)["cli"].(map[string]any)["const"].(string)
				if ifConst != cli {
					continue
				}
				enum := rule["then"].(map[string]any)["properties"].(map[string]any)["effort"].(map[string]any)["enum"].([]any)
				if !effortEnumContains(enum, effort) {
					errs = append(errs, fmt.Sprintf("effort %v not accepted for cli %q", effort, cli))
				}
			}
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

func enumStrings(raw []any) []string {
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
