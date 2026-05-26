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
