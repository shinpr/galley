package runner

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/shinpr/galley/internal/task"
)

// ClaudeOptions contains the task-derived settings needed to construct a Claude Code invocation.
type ClaudeOptions struct {
	Model             string
	Effort            string
	PromptMode        string
	MaxBudgetUSD      float64
	MaxTurns          int
	PermissionMode    string
	WorkDir           string
	SystemPromptFile  string
	JSONSchemaFile    string
	SettingsFile      string
	Prompt            string
	IncludeHookEvents bool
}

// ClaudeCommand is an execution plan suitable for exec.Command plus cmd.Dir.
type ClaudeCommand struct {
	WorkDir  string   `json:"work_dir"`
	Argv     []string `json:"argv"`
	Warnings []string `json:"warnings,omitempty"`
}

// FromTask maps a validated Galley task into Claude runner options.
func FromTask(t task.Task) ClaudeOptions {
	permissionMode := "acceptEdits"
	switch t.Scope.Permission {
	case "read-only":
		permissionMode = "plan"
	case "yolo":
		permissionMode = "bypassPermissions"
	}

	promptMode := t.Executor.PromptMode
	if promptMode == "" {
		promptMode = "replace"
	}

	return ClaudeOptions{
		Model:          t.Executor.Model,
		Effort:         t.Executor.Effort,
		PromptMode:     promptMode,
		MaxBudgetUSD:   t.Executor.MaxBudgetUSD,
		MaxTurns:       t.Executor.MaxTurns,
		PermissionMode: permissionMode,
		WorkDir:        t.Scope.CWD,
	}
}

// ClaudeArgv returns the executable argv for Claude Code.
//
// Prompt and schema files are read by Go and embedded as argument values. Shell
// substitutions are intentionally not used in this path.
func ClaudeArgv(opts ClaudeOptions) ([]string, error) {
	command, err := ClaudeCommandPlan(opts)
	if err != nil {
		return nil, err
	}
	return command.Argv, nil
}

// ClaudeCommandPlan returns the work directory, argv, and warnings for a Claude Code run.
//
// Prompt and schema files are read from paths supplied by the caller. Callers
// that cross a trust boundary should validate those paths before calling this.
func ClaudeCommandPlan(opts ClaudeOptions) (ClaudeCommand, error) {
	if opts.Prompt == "" {
		return ClaudeCommand{}, fmt.Errorf("prompt is required")
	}
	if opts.PromptMode == "" {
		opts.PromptMode = "replace"
	}

	warnings := claudeWarnings(opts)
	argv := []string{"claude", "-p", "--output-format", "stream-json", "--verbose"}

	if opts.Model != "" {
		argv = append(argv, "--model", opts.Model)
	}
	if opts.Effort != "" {
		argv = append(argv, "--effort", opts.Effort)
	}
	if opts.PermissionMode != "" {
		argv = append(argv, "--permission-mode", opts.PermissionMode)
	}
	if opts.SystemPromptFile != "" {
		systemPrompt, err := readOptionFile("system prompt", opts.SystemPromptFile)
		if err != nil {
			return ClaudeCommand{}, err
		}
		switch opts.PromptMode {
		case "replace":
			argv = append(argv, "--system-prompt", systemPrompt)
		case "append":
			argv = append(argv, "--append-system-prompt", systemPrompt)
		default:
			return ClaudeCommand{}, fmt.Errorf("unsupported prompt mode %q", opts.PromptMode)
		}
	}
	if opts.JSONSchemaFile != "" {
		schema, err := readOptionFile("JSON schema", opts.JSONSchemaFile)
		if err != nil {
			return ClaudeCommand{}, err
		}
		argv = append(argv, "--json-schema", schema)
	}
	if opts.SettingsFile != "" {
		argv = append(argv, "--settings", opts.SettingsFile)
	}
	if opts.IncludeHookEvents {
		argv = append(argv, "--include-hook-events")
	}
	if opts.MaxBudgetUSD > 0 {
		argv = append(argv, "--max-budget-usd", strconv.FormatFloat(opts.MaxBudgetUSD, 'f', -1, 64))
	}

	argv = append(argv, opts.Prompt)
	return ClaudeCommand{WorkDir: opts.WorkDir, Argv: argv, Warnings: warnings}, nil
}

// ClaudeShellPreview returns a human-oriented shell preview of a Claude Code run.
//
// The preview uses absolute prompt and schema paths so the command still works
// after changing into the task cwd.
func ClaudeShellPreview(opts ClaudeOptions) (string, []string, error) {
	if opts.Prompt == "" {
		return "", nil, fmt.Errorf("prompt is required")
	}
	if opts.PromptMode == "" {
		opts.PromptMode = "replace"
	}

	argv := []string{"claude", "-p", "--output-format", "stream-json", "--verbose"}
	if opts.Model != "" {
		argv = append(argv, "--model", opts.Model)
	}
	if opts.Effort != "" {
		argv = append(argv, "--effort", opts.Effort)
	}
	if opts.PermissionMode != "" {
		argv = append(argv, "--permission-mode", opts.PermissionMode)
	}
	if opts.SystemPromptFile != "" {
		systemPromptFile, err := absPath(opts.SystemPromptFile)
		if err != nil {
			return "", nil, err
		}
		switch opts.PromptMode {
		case "replace":
			argv = append(argv, "--system-prompt", fmt.Sprintf("$(cat %s)", shellToken(systemPromptFile)))
		case "append":
			argv = append(argv, "--append-system-prompt", fmt.Sprintf("$(cat %s)", shellToken(systemPromptFile)))
		default:
			return "", nil, fmt.Errorf("unsupported prompt mode %q", opts.PromptMode)
		}
	}
	if opts.JSONSchemaFile != "" {
		schemaFile, err := absPath(opts.JSONSchemaFile)
		if err != nil {
			return "", nil, err
		}
		argv = append(argv, "--json-schema", fmt.Sprintf("$(cat %s)", shellToken(schemaFile)))
	}
	if opts.SettingsFile != "" {
		argv = append(argv, "--settings", opts.SettingsFile)
	}
	if opts.IncludeHookEvents {
		argv = append(argv, "--include-hook-events")
	}
	if opts.MaxBudgetUSD > 0 {
		argv = append(argv, "--max-budget-usd", strconv.FormatFloat(opts.MaxBudgetUSD, 'f', -1, 64))
	}
	argv = append(argv, opts.Prompt)

	preview := ShellQuote(argv)
	if opts.WorkDir != "" {
		preview = "cd " + shellToken(opts.WorkDir) + " && " + preview
	}
	return preview, claudeWarnings(opts), nil
}

// ShellQuote formats argv as a POSIX shell command preview.
func ShellQuote(argv []string) string {
	parts := make([]string, 0, len(argv))
	for _, arg := range argv {
		if strings.HasPrefix(arg, "$(cat ") && strings.HasSuffix(arg, ")") {
			parts = append(parts, `"`+arg+`"`)
			continue
		}
		parts = append(parts, shellToken(arg))
	}
	return strings.Join(parts, " ")
}

func readOptionFile(label, path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s file %s: %w", label, path, err)
	}
	return string(data), nil
}

func absPath(path string) (string, error) {
	if filepath.IsAbs(path) {
		return path, nil
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve path %s: %w", path, err)
	}
	return absolute, nil
}

func claudeWarnings(opts ClaudeOptions) []string {
	var warnings []string
	if opts.MaxTurns > 0 {
		warnings = append(warnings, "executor.max_turns is set but Claude Code 2.1.132 does not expose --max-turns; value is not applied")
	}
	if opts.PermissionMode == "bypassPermissions" {
		warnings = append(warnings, "Claude permission mode is bypassPermissions")
	}
	return warnings
}

func shellToken(s string) string {
	if s == "" {
		return "''"
	}
	if strings.IndexFunc(s, func(r rune) bool {
		return !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("-_./:=@", r))
	}) == -1 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}
