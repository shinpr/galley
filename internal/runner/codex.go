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
	Sandbox               string
	WorkDir               string
	SystemPromptFile      string
	SystemPrompt          string
	JSONSchemaFile        string
	JSONSchema            string
	OutputLastMessageFile string
	// OutputSchemaFile is exclusively the caller-selected destination for the
	// normalized provider bytes; it is never treated as a canonical schema source.
	OutputSchemaFile string
	// AttemptDir is the attempt-scoped destination for derivative files: the
	// normalized output schema and the --output-last-message capture file.
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
		Model:   common.Model,
		Effort:  common.Effort,
		Sandbox: sandbox,
		WorkDir: common.WorkDir,
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
	opts = applyDefaultCodexSystemPrompt(opts)
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

// applyDefaultCodexSystemPrompt embeds the default Codex executor system
// prompt when the caller supplies neither a prompt body nor a prompt file.
func applyDefaultCodexSystemPrompt(opts CodexOptions) CodexOptions {
	if opts.SystemPromptFile == "" && opts.SystemPrompt == "" {
		opts.SystemPrompt = prompts.CodexExecutorFull()
	}
	return opts
}

// PrepareCodexOutputSchema normalizes opts' canonical schema and writes the
// provider artifact to destDir/filename; it is the sole Codex normalization site.
func PrepareCodexOutputSchema(opts CodexOptions, destDir, filename string) (string, error) {
	canonical, err := codexCanonicalSchema(opts)
	if err != nil {
		return "", err
	}
	normalized, err := CodexCompatibleOutputSchema(canonical)
	if err != nil {
		return "", err
	}
	path := filepath.Join(destDir, filename)
	if codexSchemaDestinationAliasesSource(opts.JSONSchemaFile, path) {
		return "", fmt.Errorf("codex output schema destination %s aliases canonical source %s: use a distinct OutputSchemaFile or AttemptDir", path, opts.JSONSchemaFile)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("create codex output-schema dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(normalized), 0o600); err != nil {
		return "", fmt.Errorf("write codex output-schema file %s: %w", path, err)
	}
	return path, nil
}

// codexSchemaDestinationAliasesSource reports whether destPath resolves to the
// same physical file as sourcePath, catching exact-path, symlink, and hard-link aliases.
func codexSchemaDestinationAliasesSource(sourcePath, destPath string) bool {
	if sourcePath == "" {
		return false
	}
	if sameCodexSchemaPath(sourcePath, destPath) {
		return true
	}
	sourceInfo, err := os.Stat(sourcePath)
	if err != nil {
		return false
	}
	destInfo, err := os.Stat(destPath)
	if err != nil {
		return false
	}
	return os.SameFile(sourceInfo, destInfo)
}

// sameCodexSchemaPath reports whether two paths are textually identical after
// cleaning, so an exact-path alias is caught even before the destination exists.
func sameCodexSchemaPath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	absA, errA := filepath.Abs(a)
	absB, errB := filepath.Abs(b)
	if errA == nil && errB == nil {
		return filepath.Clean(absA) == filepath.Clean(absB)
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

// codexCanonicalSchema returns the canonical schema bytes resolved from
// JSONSchemaFile, JSONSchema, or the embedded executor result schema default.
func codexCanonicalSchema(opts CodexOptions) (string, error) {
	if opts.JSONSchemaFile != "" {
		body, err := os.ReadFile(opts.JSONSchemaFile)
		if err != nil {
			return "", fmt.Errorf("read codex output schema file %s: %w", opts.JSONSchemaFile, err)
		}
		return string(body), nil
	}
	if opts.JSONSchema != "" {
		return opts.JSONSchema, nil
	}
	return schemas.ClaudeResult, nil
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
		normalizeCodexObjectSchema(v)
	case []any:
		for _, item := range v {
			normalizeCodexOutputSchema(item)
		}
	}
}

// normalizeCodexObjectSchema strips incompatible keywords from one object
// node, forces required properties, null-allows optionals, then recurses.
func normalizeCodexObjectSchema(m map[string]any) {
	delete(m, "allOf")
	delete(m, "pattern")
	delete(m, "uniqueItems")
	props, _ := m["properties"].(map[string]any)
	originalRequired := requiredNameSet(m["required"])
	if props != nil {
		names := make([]string, 0, len(props))
		for name := range props {
			names = append(names, name)
		}
		sort.Strings(names)
		required := make([]any, 0, len(names))
		for _, name := range names {
			required = append(required, name)
			if !originalRequired[name] {
				allowNullSchema(props[name])
			}
		}
		m["required"] = required
	}
	for _, key := range codexSubschemaObjectKeys {
		if child, ok := m[key].(map[string]any); ok {
			normalizeCodexOutputSchema(child)
		}
	}
	for _, key := range codexSubschemaArrayKeys {
		if children, ok := m[key].([]any); ok {
			for _, child := range children {
				normalizeCodexOutputSchema(child)
			}
		}
	}
	for _, key := range codexSubschemaMapKeys {
		if children, ok := m[key].(map[string]any); ok {
			for _, child := range children {
				normalizeCodexOutputSchema(child)
			}
		}
	}
}

// Single-subschema JSON Schema keyword locations Galley recurses through.
var codexSubschemaObjectKeys = []string{
	"items", "additionalProperties", "propertyNames", "contains",
	"if", "then", "else", "not", "unevaluatedItems", "unevaluatedProperties",
	"contentSchema",
}

// Array-of-subschema JSON Schema keyword locations Galley recurses through.
var codexSubschemaArrayKeys = []string{
	"prefixItems", "anyOf", "oneOf",
}

// Map<string, subschema> JSON Schema keyword locations Galley recurses through.
var codexSubschemaMapKeys = []string{
	"properties", "patternProperties", "$defs", "definitions", "dependentSchemas",
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
	return combineRolePrompt(systemPrompt, workOrder)
}

func combineRolePrompt(systemPrompt, workOrder string) string {
	if systemPrompt == "" {
		return workOrder
	}
	if workOrder == "" {
		return systemPrompt
	}
	return systemPrompt + "\n\n# Work Order\n\n" + workOrder
}

// resolveCodexAttemptFiles routes the canonical schema through the runner-owned
// normalization boundary and ensures the last-message capture path is set.
func resolveCodexAttemptFiles(opts CodexOptions) (CodexOptions, error) {
	if codexSchemaActive(opts) {
		destDir, filename, err := codexSchemaDestination(opts)
		if err != nil {
			return opts, err
		}
		path, err := PrepareCodexOutputSchema(opts, destDir, filename)
		if err != nil {
			return opts, err
		}
		opts.OutputSchemaFile = path
	}
	if opts.OutputLastMessageFile == "" && opts.AttemptDir != "" {
		opts.OutputLastMessageFile = filepath.Join(opts.AttemptDir, CodexLastMessageFilename)
	}
	return opts, nil
}

// codexSchemaActive reports whether the caller wants a Codex output schema:
// a destination or an explicit canonical schema input activates normalization.
func codexSchemaActive(opts CodexOptions) bool {
	return opts.OutputSchemaFile != "" || opts.AttemptDir != "" ||
		opts.JSONSchema != "" || opts.JSONSchemaFile != ""
}

// codexSchemaDestination resolves the derivative destination: OutputSchemaFile
// is the caller-selected path, otherwise AttemptDir/CodexOutputSchemaFilename.
func codexSchemaDestination(opts CodexOptions) (string, string, error) {
	if opts.OutputSchemaFile != "" {
		return filepath.Dir(opts.OutputSchemaFile), filepath.Base(opts.OutputSchemaFile), nil
	}
	if opts.AttemptDir != "" {
		return opts.AttemptDir, CodexOutputSchemaFilename, nil
	}
	return "", "", fmt.Errorf("codex output schema destination is required: set CodexOptions.AttemptDir or OutputSchemaFile")
}

// ExtractCodexLastMessageFile parses a Codex final-message capture.
func ExtractCodexLastMessageFile(path string) (ExecutorResult, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return ExecutorResult{}, fmt.Errorf("read codex last message %s: %w", path, err)
	}
	text := string(data)
	if result, found, parseErr := extractExecutorResultLine(text); found {
		return result, parseErr
	}
	// Fall back to the stdout-style scan so multi-line responses still surface
	// the embedded JSON result without forcing the executor to emit a strict
	// single-line message.
	return ExtractExecutorResult(text)
}

func codexWarnings(opts CodexOptions) []string {
	var warnings []string
	if opts.Sandbox == "danger-full-access" {
		warnings = append(warnings, "Codex sandbox is danger-full-access; use only inside an isolated sandbox/worktree")
	}
	return warnings
}
