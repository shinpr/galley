package runner

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/shinpr/galley/internal/task"
	"github.com/shinpr/galley/prompts"
	"github.com/shinpr/galley/schemas"
)

const GrokPromptFilename = "grok.prompt.md"

type GrokOptions struct {
	Bin, Model, Effort, PermissionMode, Sandbox, WorkDir       string
	SystemPromptFile, SystemPrompt, JSONSchemaFile, JSONSchema string
	AttemptDir, Prompt, PromptFilename                         string
}

type GrokEnvelope struct {
	Text             string          `json:"text"`
	StopReason       string          `json:"stopReason"`
	SessionID        string          `json:"sessionId"`
	StructuredOutput json.RawMessage `json:"structuredOutput"`
}

type GrokCompletionError struct {
	StopReason string
	SessionID  string
}

func (e *GrokCompletionError) Error() string {
	return fmt.Sprintf("grok did not complete: stopReason=%q sessionId=%q", e.StopReason, e.SessionID)
}

func GrokFromTask(t task.Task) GrokOptions {
	sandbox := "workspace"
	switch t.Scope.Permission {
	case "read-only":
		sandbox = "read-only"
	case "sandbox-full-access":
		sandbox = "off"
	}
	common := executorOptionsFromTask(t)
	return GrokOptions{Model: common.Model, Effort: common.Effort, PermissionMode: "bypassPermissions", Sandbox: sandbox, WorkDir: common.WorkDir}
}

func GrokCommandPlan(opts GrokOptions) (Command, error) {
	if opts.Prompt == "" {
		return Command{}, fmt.Errorf("prompt is required")
	}
	if opts.AttemptDir == "" {
		return Command{}, fmt.Errorf("attempt directory is required for grok prompt transport")
	}
	if opts.SystemPrompt == "" && opts.SystemPromptFile == "" {
		opts.SystemPrompt = prompts.GrokExecutorFull()
	}
	if opts.SystemPromptFile != "" {
		body, err := readOptionFile("system prompt", opts.SystemPromptFile)
		if err != nil {
			return Command{}, err
		}
		opts.SystemPrompt = body
	}
	if opts.JSONSchema == "" && opts.JSONSchemaFile == "" {
		opts.JSONSchema = schemas.ClaudeResult
	}
	if opts.JSONSchemaFile != "" {
		body, err := readOptionFile("JSON schema", opts.JSONSchemaFile)
		if err != nil {
			return Command{}, err
		}
		opts.JSONSchema = body
	}
	if err := os.MkdirAll(opts.AttemptDir, 0o700); err != nil {
		return Command{}, fmt.Errorf("create grok attempt directory: %w", err)
	}
	promptFilename := opts.PromptFilename
	if promptFilename == "" {
		promptFilename = GrokPromptFilename
	}
	promptPath := filepath.Join(opts.AttemptDir, promptFilename)
	promptBody := combineRolePrompt(opts.SystemPrompt, opts.Prompt)
	if err := os.WriteFile(promptPath, []byte(promptBody), 0o600); err != nil {
		return Command{}, fmt.Errorf("write grok prompt file: %w", err)
	}
	bin := opts.Bin
	if bin == "" {
		bin = "grok"
	}
	permission := opts.PermissionMode
	if permission == "" {
		permission = "bypassPermissions"
	}
	sandbox := opts.Sandbox
	if sandbox == "" {
		sandbox = "workspace"
	}
	argv := []string{bin, "--cwd", opts.WorkDir, "--permission-mode", permission, "--sandbox", sandbox, "--prompt-file", promptPath, "--verbatim", "--json-schema", strings.TrimSpace(opts.JSONSchema)}
	if opts.Model != "" {
		argv = append(argv, "--model", opts.Model)
	}
	if opts.Effort != "" {
		argv = append(argv, "--reasoning-effort", opts.Effort)
	}
	return Command{WorkDir: opts.WorkDir, Argv: argv}, nil
}

func DecodeGrokEnvelope(data []byte) (GrokEnvelope, error) {
	envelope, err := ParseGrokEnvelope(data)
	if err != nil {
		return envelope, err
	}
	if envelope.StopReason != "EndTurn" {
		return envelope, &GrokCompletionError{StopReason: envelope.StopReason, SessionID: envelope.SessionID}
	}
	if len(envelope.StructuredOutput) == 0 && strings.TrimSpace(envelope.Text) == "" {
		return envelope, fmt.Errorf("grok EndTurn envelope has no structuredOutput or text")
	}
	return envelope, nil
}

func GrokResultPayload(envelope GrokEnvelope) []byte {
	if len(envelope.StructuredOutput) > 0 && string(envelope.StructuredOutput) != "null" {
		return envelope.StructuredOutput
	}
	return []byte(envelope.Text)
}

func ParseGrokEnvelope(data []byte) (GrokEnvelope, error) {
	var envelope GrokEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return envelope, fmt.Errorf("decode grok envelope: %w", err)
	}
	if strings.TrimSpace(envelope.StopReason) == "" {
		return envelope, fmt.Errorf("decode grok envelope: stopReason is required")
	}
	if strings.TrimSpace(envelope.SessionID) == "" {
		return envelope, fmt.Errorf("decode grok envelope: sessionId is required")
	}
	return envelope, nil
}

func WriteGrokCompletionMetadata(path string, data []byte) error {
	envelope, parseErr := ParseGrokEnvelope(data)
	metadata := struct {
		Classification string `json:"classification"`
		StopReason     string `json:"stop_reason,omitempty"`
		SessionID      string `json:"session_id,omitempty"`
		ParseError     string `json:"parse_error,omitempty"`
	}{StopReason: envelope.StopReason, SessionID: envelope.SessionID}
	switch {
	case parseErr != nil:
		metadata.Classification = "malformed_envelope"
		metadata.ParseError = parseErr.Error()
	case envelope.StopReason != "EndTurn":
		metadata.Classification = "non_end_turn"
	case len(envelope.StructuredOutput) == 0 && strings.TrimSpace(envelope.Text) == "":
		metadata.Classification = "missing_result"
	default:
		metadata.Classification = "completed"
	}
	body, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, append(body, '\n'), 0o600); err != nil {
		return fmt.Errorf("write grok completion metadata: %w", err)
	}
	return nil
}

func ExtractGrokExecutorResult(data []byte) (ExecutorResult, error) {
	envelope, err := DecodeGrokEnvelope(data)
	if err != nil {
		return ExecutorResult{}, err
	}
	var result ExecutorResult
	if err := json.Unmarshal(GrokResultPayload(envelope), &result); err != nil {
		return result, fmt.Errorf("decode grok executor result: %w", err)
	}
	if err := result.Validate(); err != nil {
		return result, err
	}
	return result, nil
}
