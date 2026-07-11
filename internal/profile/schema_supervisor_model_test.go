package profile

import (
	"encoding/json"
	"fmt"
	"testing"
)

// An empty supervisor model selects the CLI default, so the generated schema
// must accept `model: ""`; a `minLength` restriction would wrongly reject it.
func TestEnvironmentSchemaSupervisorModelValidatesEmptyStringAndRejectsNonString(t *testing.T) {
	t.Parallel()
	data, err := EnvironmentJSONSchema()
	if err != nil {
		t.Fatalf("EnvironmentJSONSchema: %v", err)
	}
	var schema map[string]any
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatalf("unmarshal environment schema: %v", err)
	}

	modelSchema := supervisorModelSubschema(t, schema)
	// Guarding the structural shape catches a reintroduced minLength before the
	// value-level cases run.
	if got := modelSchema["type"]; got != "string" {
		t.Fatalf("supervisor.model type got %#v, want \"string\"", got)
	}
	if _, ok := modelSchema["minLength"]; ok {
		t.Fatalf("supervisor.model must not restrict minLength; an empty string is a valid CLI-default selector: %#v", modelSchema)
	}

	cases := []struct {
		name      string
		valueJSON string
		wantValid bool
	}{
		{name: "ExactNonEmptyModel", valueJSON: `"claude-sonnet-4-5"`, wantValid: true},
		{name: "EmptyStringSelectsCLIDefault", valueJSON: `""`, wantValid: true},
		{name: "NonStringNumberFails", valueJSON: `123`, wantValid: false},
		{name: "NonStringBoolFails", valueJSON: `true`, wantValid: false},
		{name: "NonStringNullFails", valueJSON: `null`, wantValid: false},
		{name: "NonStringArrayFails", valueJSON: `["claude-sonnet-4-5"]`, wantValid: false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var value any
			if err := json.Unmarshal([]byte(tc.valueJSON), &value); err != nil {
				t.Fatalf("unmarshal case value %s: %v", tc.valueJSON, err)
			}
			err := validateStringSubschema(modelSchema, value)
			if tc.wantValid && err != nil {
				t.Fatalf("value %s must validate against supervisor.model schema, got error: %v", tc.valueJSON, err)
			}
			if !tc.wantValid && err == nil {
				t.Fatalf("value %s must fail supervisor.model schema validation, got no error", tc.valueJSON)
			}
		})
	}
}

// supervisorModelSubschema returns the generated supervisor.model subschema,
// failing the test if any expected node is missing.
func supervisorModelSubschema(t *testing.T, schema map[string]any) map[string]any {
	t.Helper()
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("environment schema missing properties object: %#v", schema)
	}
	supervisor, ok := props["supervisor"].(map[string]any)
	if !ok {
		t.Fatalf("environment schema missing supervisor property: %#v", props)
	}
	supervisorProps, ok := supervisor["properties"].(map[string]any)
	if !ok {
		t.Fatalf("supervisor schema missing properties object: %#v", supervisor)
	}
	model, ok := supervisorProps["model"].(map[string]any)
	if !ok {
		t.Fatalf("supervisor schema missing model property: %#v", supervisorProps)
	}
	return model
}

// validateStringSubschema applies the type and minLength constraints read from
// the actual generated subschema, so a reintroduced restriction changes the
// outcome instead of silently passing.
func validateStringSubschema(subschema map[string]any, value any) error {
	if typ, ok := subschema["type"].(string); ok {
		if typ != "string" {
			return fmt.Errorf("unsupported subschema type %q for this validator", typ)
		}
		if _, isStr := value.(string); !isStr {
			return fmt.Errorf("value %#v is not a string", value)
		}
	}
	if raw, ok := subschema["minLength"]; ok {
		minLength, err := toInt(raw)
		if err != nil {
			return err
		}
		s, isStr := value.(string)
		if !isStr {
			return fmt.Errorf("minLength constraint requires a string value, got %#v", value)
		}
		if len(s) < minLength {
			return fmt.Errorf("value %q shorter than minLength %d", s, minLength)
		}
	}
	return nil
}

func toInt(raw any) (int, error) {
	switch v := raw.(type) {
	case int:
		return v, nil
	case int64:
		return int(v), nil
	case float64:
		return int(v), nil
	case json.Number:
		n, err := v.Int64()
		if err != nil {
			return 0, err
		}
		return int(n), nil
	default:
		return 0, fmt.Errorf("unsupported numeric type %T", raw)
	}
}
