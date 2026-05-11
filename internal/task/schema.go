package task

import (
	"bytes"
	"encoding/json"
)

// TaskJSONSchema returns the task YAML JSON Schema generated from the task
// contract used by structural validation.
func TaskJSONSchema() ([]byte, error) {
	schema := object(
		required("id", "mode", "status", "goal", "acceptance_criteria", "scope", "execution_policy", "worktree", "supervisor", "executor", "decisions", "risks", "attempts", "verification", "pr"),
		properties(map[string]any{
			"$schema": map[string]any{"const": "https://json-schema.org/draft/2020-12/schema"},
		}),
	)
	schema["$schema"] = "https://json-schema.org/draft/2020-12/schema"
	schema["title"] = "Galley Task YAML"
	schema["properties"] = map[string]any{
		"id":                  stringSchema("pattern", validTaskIDPattern.String()),
		"mode":                enumSchema(validModes),
		"status":              enumSchema(validStatuses),
		"goal":                stringSchema("minLength", 1),
		"acceptance_criteria": arraySchema(acceptanceCriterionSchema(), "minItems", 1),
		"files":               arraySchema(inputFileSchema()),
		"scope":               scopeSchema(),
		"execution_policy":    executionPolicySchema(),
		"worktree":            worktreeSchema(),
		"supervisor":          supervisorSchema(),
		"executor":            executorSchema(),
		"preflight":           preflightSchema(),
		"decisions":           arraySchema(decisionSchema()),
		"risks":               arraySchema(riskSchema()),
		"discussion_items":    arraySchema(discussionItemSchema()),
		"revision_requests":   arraySchema(revisionRequestSchema()),
		"attempts":            arraySchema(attemptSchema()),
		"verification":        verificationSchema(),
		"pr":                  prSchema(),
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(schema); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func acceptanceCriterionSchema() map[string]any {
	return object(
		required("id", "text", "verification", "status"),
		properties(map[string]any{
			"id":           stringSchema("minLength", 1),
			"text":         stringSchema("minLength", 1),
			"verification": stringSchema("minLength", 1),
			"status":       stringSchema(),
		}),
	)
}

func inputFileSchema() map[string]any {
	return object(
		required("source", "destination", "commit"),
		properties(map[string]any{
			"source":      stringSchema("minLength", 1),
			"destination": stringSchema("minLength", 1),
			"description": stringSchema(),
			"commit":      map[string]any{"type": "boolean"},
		}),
	)
}

func scopeSchema() map[string]any {
	return object(
		required("cwd", "allowed_paths", "forbidden_paths", "permission"),
		properties(map[string]any{
			"cwd":             stringSchema("minLength", 1),
			"allowed_paths":   arraySchema(stringSchema("minLength", 1), "minItems", 1),
			"forbidden_paths": arraySchema(stringSchema("minLength", 1)),
			"permission":      enumSchema(validPermissions),
		}),
	)
}

func executionPolicySchema() map[string]any {
	return object(
		required("loop_budget", "timeout_ms", "afk_decision_policy", "stop_on_destructive_operation", "stop_on_missing_secret", "stop_on_external_service_unavailable"),
		properties(map[string]any{
			"loop_budget":                          integerSchema("minimum", 0),
			"timeout_ms":                           integerSchema("minimum", 1),
			"afk_decision_policy":                  enumSchema(validAFKDecisionPolicies),
			"stop_on_destructive_operation":        map[string]any{"type": "boolean"},
			"stop_on_missing_secret":               map[string]any{"type": "boolean"},
			"stop_on_external_service_unavailable": map[string]any{"type": "boolean"},
		}),
	)
}

func worktreeSchema() map[string]any {
	return object(
		required("enabled", "branch", "path"),
		properties(map[string]any{
			"enabled": map[string]any{"type": "boolean"},
			"branch":  stringSchema("minLength", 1),
			"path":    stringSchema("minLength", 1),
		}),
	)
}

func supervisorSchema() map[string]any {
	return object(
		required("review_iterations"),
		properties(map[string]any{
			"review_iterations": integerSchema("minimum", 0),
		}),
	)
}

func executorSchema() map[string]any {
	return object(
		required("cli", "model", "effort", "prompt_profile", "prompt_mode", "max_budget_usd"),
		properties(map[string]any{
			"cli":            enumSchema([]string{"claude"}),
			"model":          stringSchema("minLength", 1),
			"effort":         stringSchema("minLength", 1),
			"prompt_profile": stringSchema("minLength", 1),
			"prompt_mode":    enumSchema(validPromptModes),
			"max_budget_usd": map[string]any{"type": "number", "minimum": 0},
		}),
	)
}

func decisionSchema() map[string]any {
	return object(
		required("id", "question", "chosen", "rationale", "reversibility", "needs_human_review"),
		properties(map[string]any{
			"id":                 stringSchema("minLength", 1),
			"question":           stringSchema("minLength", 1),
			"chosen":             stringSchema("minLength", 1),
			"rationale":          stringSchema("minLength", 1),
			"reversibility":      stringSchema("minLength", 1),
			"needs_human_review": map[string]any{"type": "boolean"},
		}),
	)
}

func riskSchema() map[string]any {
	return object(
		required("id", "type", "detail", "mitigation", "human_review_suggested"),
		properties(map[string]any{
			"id":                     stringSchema("minLength", 1),
			"type":                   stringSchema("minLength", 1),
			"detail":                 stringSchema("minLength", 1),
			"mitigation":             stringSchema("minLength", 1),
			"human_review_suggested": map[string]any{"type": "boolean"},
		}),
	)
}

func discussionItemSchema() map[string]any {
	return object(
		required("id", "topic", "summary", "requires_human_decision"),
		properties(map[string]any{
			"id":                      stringSchema("minLength", 1),
			"topic":                   stringSchema("minLength", 1),
			"summary":                 stringSchema("minLength", 1),
			"requires_human_decision": map[string]any{"type": "boolean"},
		}),
	)
}

func revisionRequestSchema() map[string]any {
	return object(
		required("id", "source", "text", "status"),
		properties(map[string]any{
			"id":         stringSchema("minLength", 1),
			"source":     stringSchema("minLength", 1),
			"comment_id": stringSchema(),
			"text":       stringSchema("minLength", 1),
			"status":     stringSchema("minLength", 1),
			"evidence":   stringSchema(),
		}),
	)
}

func attemptSchema() map[string]any {
	return object(
		required("number", "started_at", "completed_at", "claude_status", "supervisor_verdict", "summary"),
		properties(map[string]any{
			"number":             integerSchema("minimum", 1),
			"started_at":         stringSchema(),
			"completed_at":       stringSchema(),
			"claude_status":      stringSchema(),
			"supervisor_verdict": stringSchema(),
			"summary":            stringSchema(),
			"error": object(
				required("phase", "kind", "message"),
				properties(map[string]any{
					"phase":        stringSchema("minLength", 1),
					"kind":         stringSchema("minLength", 1),
					"message":      stringSchema("minLength", 1),
					"artifact_dir": stringSchema(),
				}),
			),
		}),
	)
}

func verificationSchema() map[string]any {
	return object(
		required("commands"),
		properties(map[string]any{
			"commands": arraySchema(object(
				required("cmd", "status", "output_excerpt"),
				properties(map[string]any{
					"cmd":            stringSchema("minLength", 1),
					"status":         stringSchema(),
					"output_excerpt": stringSchema(),
				}),
			)),
		}),
	)
}

func preflightSchema() map[string]any {
	return object(
		properties(map[string]any{
			"acceptance_skeleton": object(
				required("enabled"),
				properties(map[string]any{
					"enabled":       map[string]any{"type": "boolean"},
					"mode":          enumSchema(validPreflightSkeletonModes),
					"required":      map[string]any{"type": "boolean"},
					"allowed_paths": arraySchema(stringSchema("minLength", 1)),
					"outputs":       arraySchema(preflightOutputSchema()),
				}),
			),
		}),
	)
}

func preflightOutputSchema() map[string]any {
	return object(
		required("ac_id", "path", "kind", "purpose", "implementation_required"),
		properties(map[string]any{
			"ac_id":                   stringSchema("minLength", 1),
			"path":                    stringSchema("minLength", 1),
			"kind":                    stringSchema("minLength", 1),
			"purpose":                 stringSchema("minLength", 1),
			"satisfies":               stringSchema("minLength", 1),
			"integration_point":       stringSchema("minLength", 1),
			"implementation_required": map[string]any{"type": "boolean"},
			"template":                map[string]any{"type": "string"},
		}),
	)
}

func prSchema() map[string]any {
	return object(
		required("url", "status"),
		properties(map[string]any{
			"url":                   stringSchema(),
			"status":                stringSchema(),
			"author_login":          stringSchema(),
			"processed_comment_ids": arraySchema(stringSchema()),
		}),
	)
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
