package task

// AC: AC5 — Documentation, examples, generated schemas, and skill-bundled
// task schema references describe the new executor selection behavior and
// remain in sync.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

func repoRootFromTestFile(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// internal/task/<this_file> -> repo root is three levels up.
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func loadExampleTask(t *testing.T, rel string) Task {
	t.Helper()
	root := repoRootFromTestFile(t)
	loaded, err := Load(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("load %s: %v", rel, err)
	}
	return loaded
}

func TestExampleAFKTaskClaudeStillValid(t *testing.T) {
	t.Parallel()
	loaded := loadExampleTask(t, "examples/afk-task.yaml")
	loaded.Scope.CWD = repoRootFromTestFile(t)
	result := ValidateStructural(loaded)
	if !result.Valid() {
		t.Fatalf("examples/afk-task.yaml structural validation failed: %#v", result.Errors)
	}
	if loaded.Executor.CLI != "claude" {
		t.Fatalf("examples/afk-task.yaml executor.cli = %q, want %q", loaded.Executor.CLI, "claude")
	}
}

func TestExampleAFKTaskCodexValidates(t *testing.T) {
	t.Parallel()
	loaded := loadExampleTask(t, "examples/afk-task-codex.yaml")
	loaded.Scope.CWD = repoRootFromTestFile(t)
	result := ValidateStructural(loaded)
	if !result.Valid() {
		t.Fatalf("examples/afk-task-codex.yaml structural validation failed: %#v", result.Errors)
	}
	if loaded.Executor.CLI != "codex" {
		t.Fatalf("examples/afk-task-codex.yaml executor.cli = %q, want %q", loaded.Executor.CLI, "codex")
	}
	if loaded.Executor.PromptProfile != "codex-executor-v1" {
		t.Fatalf("examples/afk-task-codex.yaml executor.prompt_profile = %q, want %q", loaded.Executor.PromptProfile, "codex-executor-v1")
	}
}

func TestSkillBundledTaskSchemaReferenceCoversCodexCLI(t *testing.T) {
	t.Parallel()
	root := repoRootFromTestFile(t)

	generated, err := TaskJSONSchema()
	if err != nil {
		t.Fatalf("TaskJSONSchema: %v", err)
	}
	bundled, err := os.ReadFile(filepath.Join(root, TaskSchemaPath))
	if err != nil {
		t.Fatalf("read skill-bundled task.schema.json: %v", err)
	}
	var generatedDoc, bundledDoc any
	if err := json.Unmarshal(generated, &generatedDoc); err != nil {
		t.Fatalf("generated task schema is invalid JSON: %v", err)
	}
	if err := json.Unmarshal(bundled, &bundledDoc); err != nil {
		t.Fatalf("skill-bundled task schema is invalid JSON: %v", err)
	}
	if !reflect.DeepEqual(generatedDoc, bundledDoc) {
		t.Fatalf("generated task schema differs from %s; regenerate the skill-bundled reference schema from TaskJSONSchema", TaskSchemaPath)
	}

	want := []string{"claude", "codex"}
	for label, data := range map[string][]byte{
		"generated":          generated,
		"skill-bundled task": bundled,
	} {
		var doc map[string]any
		if err := json.Unmarshal(data, &doc); err != nil {
			t.Fatalf("%s: not valid JSON: %v", label, err)
		}
		props, _ := doc["properties"].(map[string]any)
		exec, _ := props["executor"].(map[string]any)
		execProps, _ := exec["properties"].(map[string]any)
		cliNode, _ := execProps["cli"].(map[string]any)
		rawEnum, _ := cliNode["enum"].([]any)
		got := []string{}
		for _, v := range rawEnum {
			s, _ := v.(string)
			got = append(got, s)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s executor.cli enum = %#v, want %#v", label, got, want)
		}
	}
}
