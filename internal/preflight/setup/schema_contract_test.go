package setup_test

import (
	"encoding/json"
	"testing"

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
