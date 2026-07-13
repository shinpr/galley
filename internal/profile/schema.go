package profile

import (
	"bytes"
	"encoding/json"

	"github.com/shinpr/galley/internal/daemonconfig"
	"github.com/shinpr/galley/internal/provider"
)

const (
	QualitySchemaPath     = "plugins/galley/skills/galley/references/quality.schema.json"
	EnvironmentSchemaPath = "plugins/galley/skills/galley/references/environment.schema.json"
)

func QualityJSONSchema() ([]byte, error) {
	schema := object(
		required("id", "required_checks", "review_dimensions", "evidence_requirements", "pass_policy"),
		properties(map[string]any{
			"id": stringSchema("minLength", 1, "description", "Repository quality profile identifier."),
			"required_checks": arraySchema(object(
				required("id", "preferred_commands", "required"),
				properties(map[string]any{
					"id":                 stringSchema("minLength", 1),
					"preferred_commands": arraySchema(stringSchema("minLength", 1)),
					"required":           boolSchema("default", true),
				}),
			)),
			"review_dimensions": arraySchema(object(
				required("id", "weight", "required", "pass"),
				properties(map[string]any{
					"id":       stringSchema("minLength", 1),
					"weight":   integerSchema("minimum", 0),
					"required": boolSchema("default", true),
					"pass":     stringSchema("minLength", 1),
				}),
			)),
			"evidence_requirements": object(
				required("file_line_references", "command_outputs"),
				properties(map[string]any{
					"file_line_references": boolSchema("default", true),
					"command_outputs":      boolSchema("default", true),
				}),
			),
			"pass_policy": object(
				required("required_dimensions_must_pass", "min_score", "blocking_severities"),
				properties(map[string]any{
					"required_dimensions_must_pass": boolSchema("default", true),
					"min_score":                     integerSchema("minimum", 0, "maximum", 100, "default", 85),
					"blocking_severities":           arraySchema(enumSchema([]string{"critical", "high", "medium", "low"}), "default", []string{"critical", "high", "medium"}),
				}),
			),
		}),
	)
	schema["$schema"] = "https://json-schema.org/draft/2020-12/schema"
	schema["title"] = "Galley Quality Profile YAML"
	return marshalSchema(schema)
}

func EnvironmentJSONSchema() ([]byte, error) {
	schema := object(
		// commands is not required here: ValidateEnvironment treats an empty
		// commands map as a warning, not an error, so an editor validating
		// against this schema must agree and not hard-fail a commandless profile.
		required("id", "cwd", "constraints"),
		properties(map[string]any{
			"id":         stringSchema("minLength", 1, "description", "Repository environment profile identifier."),
			"cwd":        stringSchema("minLength", 1, "description", "Absolute path to the target repository."),
			"commands":   map[string]any{"type": "object", "additionalProperties": stringSchema("minLength", 1)},
			"executor":   executorSchema(),
			"supervisor": supervisorSchema(),
			"required_checks": object(
				properties(map[string]any{
					"shell":      enumSchema([]string{"auto", "sh", "bash", "cmd", "powershell", "pwsh"}),
					"shell_path": stringSchema("minLength", 1, "pattern", `^\S(?:.*\S)?$`, "description", "Explicit executable path override for required-check shell selection. When the basename is one of `bash`, `sh`, `cmd.exe`, `powershell.exe`, or `pwsh.exe` (case-insensitive, optional `.exe`, either `/` or `\\` separators), `shell_path` may stand alone and Galley infers the invocation style from the executable name. When both `shell` and `shell_path` are set, `shell_path` takes precedence as the more specific executable selection and Galley will not invoke that executable using an incompatible style from `shell`. When the basename is not recognized, an explicit `required_checks.shell` kind is required as fallback metadata. In Windows YAML, escape backslashes only in double-quoted strings; single-quoted or plain scalars use single backslashes. Leading and trailing whitespace is invalid."),
				}),
			),
			"constraints": object(
				required("network", "secrets_policy", "destructive_commands"),
				properties(map[string]any{
					"network":              stringSchema("minLength", 1, "default", "approval_required"),
					"secrets_policy":       stringSchema("minLength", 1, "default", "never_read_env_files"),
					"destructive_commands": stringSchema("minLength", 1, "default", "deny"),
				}),
			),
			"pr": object(
				properties(map[string]any{
					"enabled": boolSchema("default", true),
					"base":    stringSchema("default", "main"),
					"comments": object(
						properties(map[string]any{
							"enabled": boolSchema("default", true),
							"reply":   boolSchema("default", true),
						}),
					),
				}),
				defaultValue(map[string]any{
					"enabled": true,
					"base":    "main",
					"comments": map[string]any{
						"enabled": true,
						"reply":   true,
					},
				}),
			),
			"worktree": object(
				properties(map[string]any{
					"cleanup": boolSchema("default", true),
				}),
				defaultValue(map[string]any{"cleanup": true}),
			),
			"setup": object(
				required("commands"),
				properties(map[string]any{
					"commands": arraySchema(object(
						required("run"),
						properties(map[string]any{
							"run": stringSchema("minLength", 1, "maxLength", MaxSetupCommandRunLength),
							"why": stringSchema("minLength", 1, "maxLength", MaxSetupCommandWhyLength),
						}),
					), "minItems", 1),
				}),
			),
		}),
	)
	schema["$schema"] = "https://json-schema.org/draft/2020-12/schema"
	schema["title"] = "Galley Environment Profile YAML"
	return marshalSchema(schema)
}

func executorSchema() map[string]any {
	m := object(
		properties(map[string]any{
			"default_cli": enumSchema(append([]string{""}, provider.ExecutorIDs()...)),
			"model":       stringSchema("description", "Optional model name passed unchanged to the selected executor CLI. Omit or leave empty to use the CLI default; accepted values depend on the provider."),
			"effort":      executorEffortBaseSchema(),
		}),
	)
	m["allOf"] = executorEffortSchemas()
	return m
}

func executorEffortBaseSchema() map[string]any {
	m := enumSchema(append([]string{""}, provider.ExecutorEfforts()...))
	m["description"] = "Optional reasoning effort used as a repository runtime default. Empty leaves resolution to the task or Galley's built-in default; the effective provider is validated before setup, skeleton, or implementation."
	return m
}

func executorEffortSchemas() []any {
	return roleEffortSchemas(func(d provider.Descriptor) bool { return d.Executor })
}

// supervisorSchema uses the provider union as its base and narrows effort when default_cli is selected.
func supervisorSchema() map[string]any {
	m := object(
		properties(map[string]any{
			"default_cli": enumSchema(append([]string{""}, daemonconfig.SupervisorCLIs()...)),
			"model":       stringSchema("description", "Optional model name passed unchanged to the selected supervisor CLI. Omit or leave empty to use the CLI default; accepted values depend on the provider."),
			"effort":      supervisorEffortBaseSchema(),
		}),
	)
	m["allOf"] = supervisorEffortSchemas()
	return m
}

func supervisorEffortBaseSchema() map[string]any {
	m := enumSchema(append([]string{""}, provider.SupervisorEfforts()...))
	m["description"] = "Optional reasoning effort. Empty keeps the CLI default; the effective provider is validated before review."
	return m
}

func supervisorEffortSchemas() []any {
	return roleEffortSchemas(func(d provider.Descriptor) bool { return d.Supervisor })
}

func roleEffortSchemas(include func(provider.Descriptor) bool) []any {
	var schemas []any
	for _, descriptor := range provider.All() {
		if !include(descriptor) {
			continue
		}
		efforts := append([]string{""}, provider.EffortsForTransport(descriptor.Transport)...)
		schemas = append(schemas, map[string]any{
			"if": map[string]any{
				"properties": map[string]any{"default_cli": map[string]any{"const": descriptor.ID}},
				"required":   []string{"default_cli"},
			},
			"then": map[string]any{
				"properties": map[string]any{"effort": enumSchema(efforts)},
			},
		})
	}
	return schemas
}

func marshalSchema(schema map[string]any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(schema); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func object(opts ...func(map[string]any)) map[string]any {
	m := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

func required(fields ...string) func(map[string]any) {
	return func(m map[string]any) {
		m["required"] = fields
	}
}

func properties(props map[string]any) func(map[string]any) {
	return func(m map[string]any) {
		m["properties"] = props
	}
}

func defaultValue(value any) func(map[string]any) {
	return func(m map[string]any) {
		m["default"] = value
	}
}

func stringSchema(kv ...any) map[string]any {
	m := map[string]any{"type": "string"}
	applyKV(m, kv...)
	return m
}

func integerSchema(kv ...any) map[string]any {
	m := map[string]any{"type": "integer"}
	applyKV(m, kv...)
	return m
}

func boolSchema(kv ...any) map[string]any {
	m := map[string]any{"type": "boolean"}
	applyKV(m, kv...)
	return m
}

func enumSchema(values []string) map[string]any {
	return map[string]any{"type": "string", "enum": values}
}

func arraySchema(item any, kv ...any) map[string]any {
	m := map[string]any{"type": "array", "items": item}
	applyKV(m, kv...)
	return m
}

func applyKV(m map[string]any, kv ...any) {
	for i := 0; i+1 < len(kv); i += 2 {
		key, ok := kv[i].(string)
		if ok {
			m[key] = kv[i+1]
		}
	}
}
