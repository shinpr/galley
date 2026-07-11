package profile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

func profileRepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func assertGeneratedSchemaMatchesReference(t *testing.T, name, relPath string, generated []byte) {
	t.Helper()
	reference, err := os.ReadFile(filepath.Join(profileRepoRoot(t), relPath))
	if err != nil {
		t.Fatalf("read %s reference schema: %v", name, err)
	}
	var got, want any
	if err := json.Unmarshal(generated, &got); err != nil {
		t.Fatalf("generated %s schema is invalid JSON: %v", name, err)
	}
	if err := json.Unmarshal(reference, &want); err != nil {
		t.Fatalf("reference %s schema is invalid JSON: %v", name, err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("generated %s schema differs from %s; regenerate the reference schema from the Go schema builder", name, relPath)
	}
}

func TestGeneratedProfileSchemasMatchSkillReferences(t *testing.T) {
	t.Parallel()
	quality, err := QualityJSONSchema()
	if err != nil {
		t.Fatalf("QualityJSONSchema: %v", err)
	}
	environment, err := EnvironmentJSONSchema()
	if err != nil {
		t.Fatalf("EnvironmentJSONSchema: %v", err)
	}
	assertGeneratedSchemaMatchesReference(t, "quality", QualitySchemaPath, quality)
	assertGeneratedSchemaMatchesReference(t, "environment", EnvironmentSchemaPath, environment)
}

// AC3 (schema): supervisor.model is documented as an optional override that
// Galley omits when absent or empty. The environment schema must therefore
// describe it as a plain free-form string with no minLength floor, so
// supervisor.model: "" is structurally permitted rather than rejected. A
// minLength: 1 here would conflict with the "absent or empty omits the option"
// contract, so this regression guards against reintroducing it.
func TestEnvironmentJSONSchemaAllowsEmptySupervisorModelStructurally(t *testing.T) {
	t.Parallel()
	data, err := EnvironmentJSONSchema()
	if err != nil {
		t.Fatalf("EnvironmentJSONSchema: %v", err)
	}
	var schema map[string]any
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatalf("environment schema is invalid JSON: %v", err)
	}
	props := schema["properties"].(map[string]any)
	supervisor := props["supervisor"].(map[string]any)
	supervisorProps := supervisor["properties"].(map[string]any)
	model, ok := supervisorProps["model"].(map[string]any)
	if !ok {
		t.Fatalf("supervisor.model must be present in the environment schema: %#v", supervisorProps)
	}
	if got := model["type"]; got != "string" {
		t.Fatalf("supervisor.model type got %#v, want \"string\"", got)
	}
	if _, hasMin := model["minLength"]; hasMin {
		t.Fatalf("supervisor.model must not declare minLength; empty string must stay structurally permitted: %#v", model)
	}
}
