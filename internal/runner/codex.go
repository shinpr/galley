package runner

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/shinpr/galley/internal/task"
	"github.com/shinpr/galley/prompts"
	"github.com/shinpr/galley/schemas"
)

// CodexOutputSchemaFilename is the attempt-scoped filename Galley writes when
// the caller supplies the executor result schema as inline content rather than
// a real file. `codex exec --output-schema` requires a real path, so Galley
// materializes the embedded schema here before invoking the CLI.
const CodexOutputSchemaFilename = "codex.output-schema.json"

// CodexLastMessageFilename is the attempt-scoped filename Galley requests for
// `codex exec --output-last-message`. The Codex CLI writes the final assistant
// message to this file, which the daemon then parses as the structured
// executor result (preferred over JSONL stdout for completed/hard_stop
// fidelity).
const CodexLastMessageFilename = "codex.last-message.txt"

// CodexOptions contains the task-derived settings needed to construct a Codex executor invocation.
//
// The Codex executor uses a provider-specific executor prompt that preserves
// the Claude executor contract while following the shorter Codex supervisor
// prompt shape. The system prompt is delivered to the `codex exec` CLI via
// stdin together with the work order prompt because the Codex CLI has no
// dedicated --system-prompt flag.
//
// The argv we build is constrained by the upstream `codex exec` CLI surface:
// reasoning effort is delivered through the generic `-c model_reasoning_effort=...`
// config override because `codex exec` rejects an `--effort` flag.
type CodexOptions struct {
	Bin                   string
	Model                 string
	Effort                string
	PromptMode            string
	Sandbox               string
	WorkDir               string
	SystemPromptFile      string
	SystemPrompt          string
	JSONSchemaFile        string
	JSONSchema            string
	OutputLastMessageFile string
	OutputSchemaFile      string
	// AttemptDir, when set, lets CodexCommandPlan materialize attempt-scoped
	// derivative files (the output schema file when only embedded schema
	// content is available, and the --output-last-message capture file) so the
	// upstream `codex exec` CLI flags receive real paths.
	AttemptDir string
	Prompt     string
}

// CodexFromTask maps a validated Galley task into Codex runner options.
//
// Galley scope.permission maps to the Codex --sandbox flag: read-only -> "read-only",
// edit -> "workspace-write", sandbox-full-access -> "danger-full-access".
func CodexFromTask(t task.Task) CodexOptions {
	sandbox := "workspace-write"
	switch t.Scope.Permission {
	case "read-only":
		sandbox = "read-only"
	case "sandbox-full-access":
		sandbox = "danger-full-access"
	}

	common := executorOptionsFromTask(t)

	return CodexOptions{
		Model:      common.Model,
		Effort:     common.Effort,
		PromptMode: common.PromptMode,
		Sandbox:    sandbox,
		WorkDir:    common.WorkDir,
	}
}

// CodexArgv returns the executable argv for the Codex CLI.
func CodexArgv(opts CodexOptions) ([]string, error) {
	plan, err := CodexCommandPlan(opts)
	if err != nil {
		return nil, err
	}
	return plan.Argv, nil
}

// CodexCommandPlan returns the work directory, argv, stdin, and warnings for a Codex executor run.
//
// When no system prompt is supplied, the built-in Codex executor prompt is
// used. The Codex CLI does not accept --system-prompt; the system prompt is
// concatenated with the work order prompt and delivered through stdin. The
// resulting Command.Stdin is the effective combined prompt the CLI sees.
func CodexCommandPlan(opts CodexOptions) (Command, error) {
	if opts.Prompt == "" {
		return Command{}, fmt.Errorf("prompt is required")
	}
	if opts.PromptMode == "" {
		opts.PromptMode = "replace"
	}
	var err error
	opts, err = withDefaultEmbeddedCodexOptions(opts)
	if err != nil {
		return Command{}, err
	}
	resolvedOpts, err := resolveCodexAttemptFiles(opts)
	if err != nil {
		return Command{}, err
	}
	opts = resolvedOpts

	bin := opts.Bin
	if bin == "" {
		bin = "codex"
	}
	sandbox := opts.Sandbox
	if sandbox == "" {
		sandbox = "workspace-write"
	}

	systemPrompt := opts.SystemPrompt
	if opts.SystemPromptFile != "" {
		body, err := readOptionFile("system prompt", opts.SystemPromptFile)
		if err != nil {
			return Command{}, err
		}
		systemPrompt = body
	}

	switch opts.PromptMode {
	case "replace", "append":
		// Codex inlines the system prompt via stdin. The CLI has no distinct
		// append-system-prompt surface, so append is accepted by the task
		// contract and surfaced as a warning below.
	default:
		return Command{}, fmt.Errorf("unsupported prompt mode %q", opts.PromptMode)
	}

	combined := combinePromptForCodex(systemPrompt, opts.Prompt)

	argv := []string{bin, "exec", "--cd", opts.WorkDir, "--sandbox", sandbox, "--json"}
	if opts.OutputSchemaFile != "" {
		argv = append(argv, "--output-schema", opts.OutputSchemaFile)
	}
	if opts.OutputLastMessageFile != "" {
		argv = append(argv, "--output-last-message", opts.OutputLastMessageFile)
	}
	if opts.Model != "" {
		argv = append(argv, "--model", opts.Model)
	}
	// `codex exec` does not expose a top-level --effort flag. The reasoning
	// effort hint is delivered through the generic config override surface
	// (`-c model_reasoning_effort=<value>`) so the executor still honors the
	// task's executor.effort selection without invoking a flag that the local
	// `codex exec --help` rejects.
	if opts.Effort != "" {
		argv = append(argv, "-c", fmt.Sprintf("model_reasoning_effort=%q", opts.Effort))
	}
	argv = append(argv, "-")

	warnings := codexWarnings(opts)

	return Command{
		WorkDir:  opts.WorkDir,
		Argv:     argv,
		Stdin:    combined,
		Warnings: warnings,
	}, nil
}

// CodexEffectiveSystemPrompt returns the system prompt content that
// CodexCommandPlan would use, after applying file/string overrides and the
// default embedded prompt. This is exposed so AC3 parity tests can assert the
// effective system prompt without parsing the Stdin envelope.
func CodexEffectiveSystemPrompt(opts CodexOptions) (string, error) {
	if opts.SystemPromptFile != "" {
		return readOptionFile("system prompt", opts.SystemPromptFile)
	}
	if opts.SystemPrompt != "" {
		return opts.SystemPrompt, nil
	}
	return prompts.CodexExecutorFull(), nil
}

func withDefaultEmbeddedCodexOptions(opts CodexOptions) (CodexOptions, error) {
	if opts.SystemPromptFile == "" && opts.SystemPrompt == "" {
		opts.SystemPrompt = prompts.CodexExecutorFull()
	}
	if opts.JSONSchemaFile == "" && opts.JSONSchema == "" {
		schema, err := CodexExecutorResultSchema()
		if err != nil {
			return opts, err
		}
		opts.JSONSchema = schema
	}
	return opts, nil
}

// CodexExecutorResultSchema returns the executor result schema shape accepted
// by `codex exec --output-schema`. Codex currently rejects JSON Schema
// conditionals such as allOf/if/then/else in response_format schemas and
// requires every object property to be listed in required. The runner keeps
// optional semantics by making originally optional properties nullable before
// invoking Codex. Galley still validates the parsed result with
// ClaudeResult.Validate(), which preserves semantic requirements after the
// model responds.
func CodexExecutorResultSchema() (string, error) {
	return CodexCompatibleOutputSchema(schemas.ClaudeResult)
}

// CodexCompatibleOutputSchema adapts Galley's persisted JSON schemas for the
// stricter response_format subset used by `codex exec --output-schema`.
func CodexCompatibleOutputSchema(schema string) (string, error) {
	var doc any
	if err := json.Unmarshal([]byte(schema), &doc); err != nil {
		return "", fmt.Errorf("decode codex output schema: %w", err)
	}
	normalizeCodexOutputSchema(doc)
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode codex output schema: %w", err)
	}
	return string(out) + "\n", nil
}

func normalizeCodexOutputSchema(node any) {
	switch v := node.(type) {
	case map[string]any:
		delete(v, "allOf")
		props, _ := v["properties"].(map[string]any)
		originalRequired := requiredNameSet(v["required"])
		if props != nil {
			names := make([]string, 0, len(props))
			for name := range props {
				names = append(names, name)
			}
			sort.Strings(names)
			required := make([]any, 0, len(names))
			for _, name := range names {
				required = append(required, name)
				prop := props[name]
				if !originalRequired[name] {
					allowNullSchema(prop)
				}
				normalizeCodexOutputSchema(prop)
			}
			v["required"] = required
		}
		if items, ok := v["items"]; ok {
			normalizeCodexOutputSchema(items)
		}
	case []any:
		for _, item := range v {
			normalizeCodexOutputSchema(item)
		}
	}
}

func requiredNameSet(raw any) map[string]bool {
	out := map[string]bool{}
	items, ok := raw.([]any)
	if !ok {
		return out
	}
	for _, item := range items {
		if name, ok := item.(string); ok {
			out[name] = true
		}
	}
	return out
}

func allowNullSchema(node any) {
	m, ok := node.(map[string]any)
	if !ok {
		return
	}
	if enum, ok := m["enum"].([]any); ok && !containsNull(enum) {
		m["enum"] = append(enum, nil)
	}
	switch t := m["type"].(type) {
	case string:
		if t != "null" {
			m["type"] = []any{t, "null"}
		}
	case []any:
		if !containsNull(t) {
			m["type"] = append(t, "null")
		}
	}
}

func containsNull(items []any) bool {
	for _, item := range items {
		if item == nil {
			return true
		}
		if s, ok := item.(string); ok && s == "null" {
			return true
		}
	}
	return false
}

func combinePromptForCodex(systemPrompt, workOrder string) string {
	if systemPrompt == "" {
		return workOrder
	}
	if workOrder == "" {
		return systemPrompt
	}
	return systemPrompt + "\n\n# Work Order\n\n" + workOrder
}

// resolveCodexAttemptFiles maps the JSONSchemaFile/JSONSchema fields into a
// concrete `codex exec --output-schema <file>` path and, when AttemptDir is
// available, makes sure `--output-last-message <file>` is requested as well.
//
// The Codex CLI rejects --output-schema arguments that point at non-existent
// files, so when only embedded schema content is available the runner writes
// it to attemptDir/CodexOutputSchemaFilename before invoking the CLI. The
// last-message path is similarly attempt-scoped so per-attempt evidence is
// preserved alongside the other run artifacts. Callers that supply their own
// OutputSchemaFile or OutputLastMessageFile keep those values intact.
func resolveCodexAttemptFiles(opts CodexOptions) (CodexOptions, error) {
	if opts.OutputSchemaFile == "" {
		switch {
		case opts.JSONSchemaFile != "":
			opts.OutputSchemaFile = opts.JSONSchemaFile
		case opts.AttemptDir != "" && opts.JSONSchema != "":
			path := filepath.Join(opts.AttemptDir, CodexOutputSchemaFilename)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				return opts, fmt.Errorf("create codex output-schema dir: %w", err)
			}
			if err := os.WriteFile(path, []byte(opts.JSONSchema), 0o600); err != nil {
				return opts, fmt.Errorf("write codex output-schema file %s: %w", path, err)
			}
			opts.OutputSchemaFile = path
		}
	}
	if opts.OutputLastMessageFile == "" && opts.AttemptDir != "" {
		opts.OutputLastMessageFile = filepath.Join(opts.AttemptDir, CodexLastMessageFilename)
	}
	return opts, nil
}

// ExtractCodexLastMessageFile parses the structured executor result from a
// `codex exec --output-last-message` capture file. The Codex CLI writes the
// final assistant message verbatim, so the captured content typically contains
// a single JSON object that already matches the executor result schema. The
// parser reuses the same line-level extractor as the Claude stdout path so
// final messages that embed the JSON inside surrounding prose still resolve
// to a validated ClaudeResult.
func ExtractCodexLastMessageFile(path string) (ClaudeResult, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return ClaudeResult{}, fmt.Errorf("read codex last message %s: %w", path, err)
	}
	text := string(data)
	if result, found, parseErr := extractClaudeResultLine(text); found {
		return result, parseErr
	}
	// Fall back to the stdout-style scan so multi-line responses still surface
	// the embedded JSON result without forcing the executor to emit a strict
	// single-line message.
	return ExtractClaudeResult(text)
}

func codexWarnings(opts CodexOptions) []string {
	var warnings []string
	if opts.PromptMode == "append" {
		warnings = append(warnings, "executor.prompt_mode=append has the same effect as replace for codex exec; system prompt and work order are concatenated through stdin")
	}
	if opts.Sandbox == "danger-full-access" {
		warnings = append(warnings, "Codex sandbox is danger-full-access; use only inside an isolated sandbox/worktree")
	}
	return warnings
}
