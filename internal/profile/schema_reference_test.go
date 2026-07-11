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

func TestEnvironmentSchemaExposesSupervisorModelAsOptionalString(t *testing.T) {
	t.Parallel()
	// AC5: supervisor.model must appear as an optional free-form string, not a
	// Galley-defined enum, so authors are not misled about its validation
	// semantics. It must also stay outside the object's required list.
	environment, err := EnvironmentJSONSchema()
	if err != nil {
		t.Fatalf("EnvironmentJSONSchema: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(environment, &doc); err != nil {
		t.Fatalf("environment schema is invalid JSON: %v", err)
	}
	props := doc["properties"].(map[string]any)
	supervisor := props["supervisor"].(map[string]any)
	supProps := supervisor["properties"].(map[string]any)
	model, ok := supProps["model"].(map[string]any)
	if !ok {
		t.Fatalf("supervisor.model missing from environment schema: %v", supProps)
	}
	if model["type"] != "string" {
		t.Fatalf("supervisor.model type got %v, want string", model["type"])
	}
	if _, hasEnum := model["enum"]; hasEnum {
		t.Fatalf("supervisor.model must not define an enum: %v", model)
	}
	if _, hasDesc := model["description"]; !hasDesc {
		t.Fatalf("supervisor.model must document its provider-defined semantics")
	}
	if req, ok := supervisor["required"]; ok {
		for _, field := range req.([]any) {
			if field == "model" {
				t.Fatalf("supervisor.model must remain optional")
			}
		}
	}
}
