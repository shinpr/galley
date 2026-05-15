package runner

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/shinpr/galley/internal/task"
	"github.com/shinpr/galley/prompts"
	"github.com/shinpr/galley/schemas"
)

// ClaudeOptions contains the task-derived settings needed to construct a Claude Code invocation.
type ClaudeOptions struct {
	Bin               string
	Model             string
	Effort            string
	PromptMode        string
	MaxBudgetUSD      float64
	PermissionMode    string
	WorkDir           string
	SystemPromptFile  string
	SystemPrompt      string
	JSONSchemaFile    string
	JSONSchema        string
	SettingsFile      string
	Prompt            string
	IncludeHookEvents bool
	PluginDirs        []string
}

// Command is an execution plan suitable for exec.Command plus cmd.Dir.
type Command struct {
	WorkDir  string   `json:"work_dir"`
	Argv     []string `json:"argv"`
	Stdin    string   `json:"stdin,omitempty"`
	Env      []string `json:"env,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

// FromTask maps a validated Galley task into Claude runner options.
func FromTask(t task.Task) ClaudeOptions {
	permissionMode := "acceptEdits"
	switch t.Scope.Permission {
	case "read-only":
		permissionMode = "plan"
	case "sandbox-full-access":
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
		MaxBudgetUSD:   t.Executor.MaxBudgetUSDValue(),
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
// When no prompt or schema path/content is supplied, the built-in Galley
// executor prompt and result schema are embedded into argv. Caller-supplied
// prompt and schema file paths are read before execution.
func ClaudeCommandPlan(opts ClaudeOptions) (Command, error) {
	if opts.Prompt == "" {
		return Command{}, fmt.Errorf("prompt is required")
	}
	if opts.PromptMode == "" {
		opts.PromptMode = "replace"
	}
	opts = withDefaultEmbeddedOptions(opts)

	warnings := claudeWarnings(opts)
	argv, err := buildClaudeArgv(opts, func(label, path string) (string, error) {
		return readOptionFile(label, path)
	})
	if err != nil {
		return Command{}, err
	}
	return Command{WorkDir: opts.WorkDir, Argv: argv, Warnings: warnings}, nil
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
	opts = withDefaultEmbeddedOptions(opts)

	argv, err := buildClaudeArgv(opts, func(_ string, path string) (string, error) {
		absolute, err := absPath(path)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("$(cat %s)", shellToken(absolute)), nil
	})
	if err != nil {
		return "", nil, err
	}
	preview := ShellQuote(argv)
	if opts.WorkDir != "" {
		preview = "cd " + shellToken(opts.WorkDir) + " && " + preview
	}
	return preview, claudeWarnings(opts), nil
}

func buildClaudeArgv(opts ClaudeOptions, fileValue func(label, path string) (string, error)) ([]string, error) {
	bin := opts.Bin
	if bin == "" {
		bin = "claude"
	}
	argv := []string{bin, "-p", "--output-format", "stream-json", "--verbose"}
	if opts.Model != "" {
		argv = append(argv, "--model", opts.Model)
	}
	if opts.Effort != "" {
		argv = append(argv, "--effort", opts.Effort)
	}
	if opts.PermissionMode != "" {
		argv = append(argv, "--permission-mode", opts.PermissionMode)
	}
	if opts.SystemPromptFile != "" || opts.SystemPrompt != "" {
		systemPrompt := opts.SystemPrompt
		if opts.SystemPromptFile != "" {
			var err error
			systemPrompt, err = fileValue("system prompt", opts.SystemPromptFile)
			if err != nil {
				return nil, err
			}
		}
		switch opts.PromptMode {
		case "replace":
			argv = append(argv, "--system-prompt", systemPrompt)
		case "append":
			argv = append(argv, "--append-system-prompt", systemPrompt)
		default:
			return nil, fmt.Errorf("unsupported prompt mode %q", opts.PromptMode)
		}
	}
	if opts.JSONSchemaFile != "" || opts.JSONSchema != "" {
		schema := opts.JSONSchema
		if opts.JSONSchemaFile != "" {
			var err error
			schema, err = fileValue("JSON schema", opts.JSONSchemaFile)
			if err != nil {
				return nil, err
			}
		}
		argv = append(argv, "--json-schema", schema)
	}
	if opts.SettingsFile != "" {
		argv = append(argv, "--settings", opts.SettingsFile)
	}
	for _, dir := range opts.PluginDirs {
		if dir != "" {
			argv = append(argv, "--plugin-dir", dir)
		}
	}
	if opts.IncludeHookEvents {
		argv = append(argv, "--include-hook-events")
	}
	if opts.MaxBudgetUSD > 0 {
		argv = append(argv, "--max-budget-usd", strconv.FormatFloat(opts.MaxBudgetUSD, 'f', -1, 64))
	}
	argv = append(argv, opts.Prompt)
	return argv, nil
}

func withDefaultEmbeddedOptions(opts ClaudeOptions) ClaudeOptions {
	if opts.SystemPromptFile == "" && opts.SystemPrompt == "" {
		opts.SystemPrompt = prompts.ClaudeExecutorFull()
	}
	if opts.JSONSchemaFile == "" && opts.JSONSchema == "" {
		opts.JSONSchema = schemas.ClaudeResult
	}
	return opts
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
	if opts.PermissionMode == "bypassPermissions" {
		warnings = append(warnings, "Claude permission mode is bypassPermissions; use only inside an isolated sandbox/worktree")
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
