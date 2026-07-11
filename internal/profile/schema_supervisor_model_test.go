package profile

import (
	"encoding/json"
	"testing"
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
