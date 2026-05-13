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
