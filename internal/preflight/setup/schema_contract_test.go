package setup_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shinpr/galley/internal/preflight/setup"
	"github.com/shinpr/galley/internal/profile"
	"github.com/shinpr/galley/internal/task"
	"github.com/shinpr/galley/schemas"
)

func TestSetupResultSchemaRequiresResultSource(t *testing.T) {
	t.Parallel()

	var schema map[string]any
	if err := json.Unmarshal([]byte(schemas.SetupResult), &schema); err != nil {
		t.Fatalf("decode setup result schema: %v", err)
	}

	assertRequiredFields(t, schema, "status", "commands", "source")
}

func assertRequiredFields(t *testing.T, raw any, fields ...string) {
	t.Helper()

	branch, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("schema branch = %#v, want object", raw)
	}
	required, ok := branch["required"].([]any)
	if !ok {
		t.Fatalf("schema branch required = %#v, want array", branch["required"])
	}
	set := make(map[string]bool, len(required))
	for _, item := range required {
		name, ok := item.(string)
		if !ok {
			t.Fatalf("required field = %#v, want string", item)
		}
		set[name] = true
	}
	for _, field := range fields {
		if !set[field] {
			t.Fatalf("schema branch does not require %q: %#v", field, required)
		}
	}
}

// TestSetupResultSchemaMatchesPersistedShape validates a fully populated persisted
// setup_result.json against the published schema so runtime and schema cannot drift.
func TestSetupResultSchemaMatchesPersistedShape(t *testing.T) {
	t.Parallel()
	runDir := t.TempDir()
	res := &setup.Result{
		Status: setup.StatusReady,
		Commands: []setup.CommandAttempt{
			{Run: "go mod download", Why: "fetch modules", Source: setup.SourceDiscovered, ExitCode: 0, StdoutExcerpt: "ok", StderrExcerpt: ""},
			{Run: "go build ./...", Why: "readiness verification", Source: setup.SourceReadinessCheck, ExitCode: 0},
		},
		SuccessfulCommands: []profile.SetupCommand{{Run: "go mod download", Why: "fetch modules"}},
		InspectedFiles:     []string{"go.mod", "go.sum"},
		ReadinessEvidence:  "discovery passed; readiness verified",
		Provider:           "claude",
		Source:             setup.SourceDiscovered,
	}
	// An explicit empty model must persist as an executor_model key the schema accepts.
	setup.ApplyExecutorIdentity(res, task.Executor{CLI: "claude", Model: "", Effort: "high"})
	if err := setup.WriteResult(runDir, res); err != nil {
		t.Fatalf("write setup result: %v", err)
	}
	savedRaw, err := os.ReadFile(filepath.Join(runDir, "setup_result.json"))
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]any
	if err := json.Unmarshal(savedRaw, &saved); err != nil {
		t.Fatalf("decode saved: %v", err)
	}
	if model, ok := saved["executor_model"]; !ok || model != "" {
		t.Fatalf("persisted executor_model must be explicit empty string, got present=%v value=%#v", ok, model)
	}
	var schema map[string]any
	if err := json.Unmarshal([]byte(schemas.SetupResult), &schema); err != nil {
		t.Fatalf("decode schema: %v", err)
	}
	if errs := validateAgainstSchemaForTest(saved, schema, "$"); len(errs) > 0 {
		t.Fatalf("saved setup_result.json does not match schema:\n  %s", strings.Join(errs, "\n  "))
	}
	props, _ := schema["properties"].(map[string]any)
	for _, requiredKey := range []string{"provider", "source", "successful_commands", "inspected_files", "readiness_evidence", "repair_guidance", "error", "executor_cli", "executor_model", "executor_effort"} {
		if _, ok := props[requiredKey]; !ok {
			t.Fatalf("schema missing property %q that the runtime persists", requiredKey)
		}
	}
}

// validateAgainstSchemaForTest walks the JSON-schema subset the setup result schema
// uses (type, required, additionalProperties:false, enum, items, properties).
func validateAgainstSchemaForTest(value any, schema map[string]any, path string) []string {
	if typ, ok := schema["type"].(string); ok && !jsonTypeMatchesForTest(typ, value) {
		return []string{fmt.Sprintf("%s: type mismatch: want %s, got %T", path, typ, value)}
	}
	errs := validateEnumForTest(value, schema, path)
	switch v := value.(type) {
	case map[string]any:
		errs = append(errs, validateObjectForTest(v, schema, path)...)
	case []any:
		if items, ok := schema["items"].(map[string]any); ok {
			for i, child := range v {
				errs = append(errs, validateAgainstSchemaForTest(child, items, fmt.Sprintf("%s[%d]", path, i))...)
			}
		}
	}
	return errs
}

func validateEnumForTest(value any, schema map[string]any, path string) []string {
	enum, ok := schema["enum"].([]any)
	if !ok {
		return nil
	}
	for _, allowed := range enum {
		if allowed == value {
			return nil
		}
	}
	return []string{fmt.Sprintf("%s: value %v not in enum %v", path, value, enum)}
}

func validateObjectForTest(v, schema map[string]any, path string) []string {
	props, _ := schema["properties"].(map[string]any)
	var errs []string
	if required, ok := schema["required"].([]any); ok {
		for _, r := range required {
			name, _ := r.(string)
			if _, has := v[name]; !has {
				errs = append(errs, fmt.Sprintf("%s: missing required property %q", path, name))
			}
		}
	}
	for key, child := range v {
		subSchema, declared := props[key].(map[string]any)
		if !declared {
			if msg := undeclaredPropertyErrorForTest(schema, path, key); msg != "" {
				errs = append(errs, msg)
			}
			continue
		}
		errs = append(errs, validateAgainstSchemaForTest(child, subSchema, path+"."+key)...)
	}
	return errs
}

// undeclaredPropertyErrorForTest reports a property the schema does not declare
// only when additionalProperties is explicitly false.
func undeclaredPropertyErrorForTest(schema map[string]any, path, key string) string {
	additional, specified := schema["additionalProperties"]
	if !specified {
		return ""
	}
	if allow, ok := additional.(bool); ok && !allow {
		return fmt.Sprintf("%s: property %q is not declared in schema and additionalProperties is false", path, key)
	}
	return ""
}

func jsonTypeMatchesForTest(want string, value any) bool {
	switch want {
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "string":
		_, ok := value.(string)
		return ok
	case "integer":
		// encoding/json decodes numbers as float64; accept whole-number floats.
		if f, ok := value.(float64); ok {
			return f == float64(int64(f))
		}
		return false
	case "number":
		_, ok := value.(float64)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "null":
		return value == nil
	}
	return true
}
