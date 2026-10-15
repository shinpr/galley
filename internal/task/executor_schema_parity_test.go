package task

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
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
	wantErr   string
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
	{name: "claude rejects minimal", cliSet: true, cli: "claude", effortSet: true, effort: "minimal", wantErr: "executor.effort for claude"},
	{name: "invalid cli", cliSet: true, cli: "opus", wantErr: "executor.cli must be one of"},
	{name: "effort only unknown", effortSet: true, effort: "turbo", wantErr: "executor.effort must be one of"},
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

	for _, tc := range executorOptionalFieldMatrix {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			schemaValid := len(validateTaskExecutorDoc(executor, tc.executorDoc())) == 0
			runtimeErrs := runtimeTaskExecutorErrors(tc)
			runtimeValid := len(runtimeErrs) == 0
			if schemaValid != runtimeValid {
				t.Fatalf("drift for %v: schema valid=%v runtime valid=%v schema_errs=%v",
					tc.executorDoc(), schemaValid, runtimeValid, validateTaskExecutorDoc(executor, tc.executorDoc()))
			}
			if schemaValid != tc.valid {
				t.Fatalf("case %q: got valid=%v, want %v", tc.name, schemaValid, tc.valid)
			}
			if !strings.Contains(strings.Join(runtimeErrs, "\n"), tc.wantErr) {
				t.Fatalf("runtime errors %#v want substring %q", runtimeErrs, tc.wantErr)
			}
		})
	}
}

func runtimeTaskExecutorErrors(c executorFieldCase) []string {
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
			LoopBudget: LoopBudget{Count: 1, Set: true},
			TimeoutMS:  1000,
		},
		Worktree: Worktree{
			Enabled: true,
			Branch:  "agent/parity",
			Path:    "../worktrees/parity",
		},
		Executor: c.taskExecutor(),
	}
	var errs []string
	for _, e := range ValidateStructural(base).Errors {
		if strings.Contains(e, "executor.") {
			errs = append(errs, e)
		}
	}
	return errs
}

func validateTaskExecutorDoc(executor, doc map[string]any) []string {
	props := executor["properties"].(map[string]any)
	var errs []string
	errs = append(errs, validateExecutorCLIField(props, doc)...)
	errs = append(errs, validateExecutorModelField(props, doc)...)
	errs = append(errs, validateExecutorEffortField(executor, props, doc)...)
	return errs
}

func validateExecutorCLIField(props, doc map[string]any) []string {
	cli, present := doc["cli"]
	if !present {
		return nil
	}
	if effortEnumContains(props["cli"].(map[string]any)["enum"].([]any), cli) {
		return nil
	}
	return []string{fmt.Sprintf("cli %v not in enum", cli)}
}

// validateExecutorModelField checks the free-form model string; empty is
// allowed, so only the type and minLength are constrained.
func validateExecutorModelField(props, doc map[string]any) []string {
	model, present := doc["model"]
	if !present {
		return nil
	}
	var errs []string
	if _, ok := model.(string); !ok {
		errs = append(errs, fmt.Sprintf("model %v is not a string", model))
	}
	min, ok := props["model"].(map[string]any)["minLength"].(float64)
	if !ok {
		return errs
	}
	if s, _ := model.(string); len(s) < int(min) {
		errs = append(errs, fmt.Sprintf("model %q shorter than minLength %v", model, min))
	}
	return errs
}

func validateExecutorEffortField(executor, props, doc map[string]any) []string {
	effort, present := doc["effort"]
	if !present {
		return nil
	}
	var errs []string
	if !effortEnumContains(props["effort"].(map[string]any)["enum"].([]any), effort) {
		errs = append(errs, fmt.Sprintf("effort %v not in base enum", effort))
	}
	// The provider-conditioned allOf applies only when cli is present and set.
	cli, _ := doc["cli"].(string)
	if cli == "" {
		return errs
	}
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
