package runner

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/shinpr/galley/internal/task"
	"github.com/shinpr/galley/prompts"
	"github.com/shinpr/galley/schemas"
)

// ClaudeSystemPromptFilename is the filename Galley uses when it materializes
// an embedded Claude system prompt onto disk for the Windows command-line
// length workaround. The file lives under the caller-supplied AttemptDir when
// provided so per-attempt evidence is preserved alongside the other run
// artifacts.
const ClaudeSystemPromptFilename = "claude_system_prompt.md"

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
	// AttemptDir lets ClaudeCommandPlan materialize the Windows system prompt
	// file under a stable per-attempt directory. It is required on Windows when
	// SystemPrompt content must be written to disk.
	AttemptDir string
}

// Command is an execution plan suitable for exec.Command plus cmd.Dir.
type Command struct {
	WorkDir string   `json:"work_dir"`
	Argv    []string `json:"argv"`
	Stdin   string   `json:"stdin,omitempty"`
	// EnvAppend contains Galley-owned per-command environment entries. It is
	// intentionally not serialized into command-plan evidence and never holds
	// the parent process environment.
	EnvAppend []string `json:"-"`
	Warnings  []string `json:"warnings,omitempty"`
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

	common := executorOptionsFromTask(t)

	return ClaudeOptions{
		Model:          common.Model,
		Effort:         common.Effort,
		PromptMode:     common.PromptMode,
		MaxBudgetUSD:   common.MaxBudgetUSD,
		PermissionMode: permissionMode,
		WorkDir:        common.WorkDir,
	}
}

// ClaudeArgv returns the executable argv for Claude Code.
//
// Prompt and schema files are read by Go and embedded as argument values on
// macOS and Linux. On Windows the work order prompt is delivered through
// stdin and the system prompt through --system-prompt-file, so the returned
// argv intentionally omits the work order and JSON schema bodies.
func ClaudeArgv(opts ClaudeOptions) ([]string, error) {
	command, err := ClaudeCommandPlan(opts)
	if err != nil {
		return nil, err
	}
	return command.Argv, nil
}

// ClaudeCommandPlan returns the work directory, argv, stdin, and warnings for
// a Claude Code run on the current host OS.
//
// macOS and Linux preserve the historical argv shape, where the system prompt,
// JSON schema, and work order prompt are passed as argv values. Windows moves
// the system prompt to --system-prompt-file (or --append-system-prompt-file)
// and delivers the work order prompt through stdin to avoid the
// CommandLineToArgvW length limit. The JSON schema body is intentionally not
// passed on argv on Windows; Galley relies on the Claude guard hook and the
// executor result validators to reject malformed output.
func ClaudeCommandPlan(opts ClaudeOptions) (Command, error) {
	return ClaudeCommandPlanForOS(opts, runtime.GOOS)
}

// ClaudeCommandPlanForOS builds a Claude Code command plan for the requested
// target OS. It is exported so tests can construct Windows-shaped plans
// regardless of the host OS.
func ClaudeCommandPlanForOS(opts ClaudeOptions, goos string) (Command, error) {
	if opts.Prompt == "" {
		return Command{}, fmt.Errorf("prompt is required")
	}
	if opts.PromptMode == "" {
		opts.PromptMode = "replace"
	}
	opts = withDefaultEmbeddedOptions(opts)

	warnings := claudeWarnings(opts)
	if goos == "windows" {
		argv, stdin, extraWarnings, err := buildClaudeArgvWindows(opts)
		if err != nil {
			return Command{}, err
		}
		warnings = append(warnings, extraWarnings...)
		return Command{WorkDir: opts.WorkDir, Argv: argv, Stdin: stdin, Warnings: warnings}, nil
	}
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
// after changing into the task cwd. The preview keeps the POSIX shell shape
// even when the actual run on Windows takes a different routing path; the
// Windows runtime behavior is documented in CHANGELOG.md.
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
	common := buildClaudeCommonArgs(opts)
	argv := append(baseClaudeArgv(opts), common.Prefix...)
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
	argv = append(argv, common.Suffix...)
	argv = append(argv, opts.Prompt)
	return argv, nil
}

type claudeCommonArgs struct {
	// Prefix must appear before prompt/schema routing flags; Suffix must appear
	// after them so POSIX keeps --system-prompt and --json-schema in the
	// historical argv position while Windows can replace those middle flags.
	Prefix []string
	Suffix []string
}

func buildClaudeCommonArgs(opts ClaudeOptions) claudeCommonArgs {
	var common claudeCommonArgs
	if opts.Model != "" {
		common.Prefix = append(common.Prefix, "--model", opts.Model)
	}
	if opts.Effort != "" {
		common.Prefix = append(common.Prefix, "--effort", opts.Effort)
	}
	if opts.PermissionMode != "" {
		common.Prefix = append(common.Prefix, "--permission-mode", opts.PermissionMode)
	}
	if opts.SettingsFile != "" {
		common.Suffix = append(common.Suffix, "--settings", opts.SettingsFile)
	}
	for _, dir := range opts.PluginDirs {
		if dir != "" {
			common.Suffix = append(common.Suffix, "--plugin-dir", dir)
		}
	}
	if opts.IncludeHookEvents {
		common.Suffix = append(common.Suffix, "--include-hook-events")
	}
	if opts.MaxBudgetUSD > 0 {
		common.Suffix = append(common.Suffix, "--max-budget-usd", strconv.FormatFloat(opts.MaxBudgetUSD, 'f', -1, 64))
	}
	return common
}

func baseClaudeArgv(opts ClaudeOptions) []string {
	bin := opts.Bin
	if bin == "" {
		bin = "claude"
	}
	return []string{bin, "-p", "--output-format", "stream-json", "--verbose"}
}

// buildClaudeArgvWindows builds the Windows-only Claude argv. It keeps
// Galley-generated long values (system prompt, work order prompt, JSON schema)
// off argv: the system prompt is delivered through --system-prompt-file or
// --append-system-prompt-file, the work order prompt is delivered through
// stdin (Claude Code reads stdin in -p/non-interactive mode without the Codex
// `-` marker), and the JSON schema body is intentionally not passed on argv.
// The Galley Claude guard hook and the executor result validators reject
// malformed final output, preserving the structured-output contract.
func buildClaudeArgvWindows(opts ClaudeOptions) ([]string, string, []string, error) {
	common := buildClaudeCommonArgs(opts)
	argv := append(baseClaudeArgv(opts), common.Prefix...)
	var warnings []string
	if opts.SystemPromptFile != "" || opts.SystemPrompt != "" {
		path, err := resolveWindowsClaudeSystemPromptFile(opts)
		if err != nil {
			return nil, "", nil, err
		}
		switch opts.PromptMode {
		case "replace":
			argv = append(argv, "--system-prompt-file", path)
		case "append":
			argv = append(argv, "--append-system-prompt-file", path)
		default:
			return nil, "", nil, fmt.Errorf("unsupported prompt mode %q", opts.PromptMode)
		}
	}
	if opts.JSONSchemaFile != "" || opts.JSONSchema != "" {
		warnings = append(warnings, "Windows runner does not pass --json-schema on argv; Galley relies on the Claude guard hook and the executor result validators to reject malformed final output")
	}
	argv = append(argv, common.Suffix...)
	// The work order prompt is delivered through stdin so Galley-generated
	// long content does not reach argv on Windows.
	return argv, opts.Prompt, warnings, nil
}

// resolveWindowsClaudeSystemPromptFile returns a real on-disk path that the
// Windows Claude command plan can pass to --system-prompt-file. Caller-supplied
// file paths are resolved by the parent process so their meaning does not
// change when the child process runs with opts.WorkDir. Embedded prompt content
// is materialized under opts.AttemptDir so the command plan does not leak
// anonymous temp directories and the per-attempt prompt evidence is preserved.
func resolveWindowsClaudeSystemPromptFile(opts ClaudeOptions) (string, error) {
	if opts.SystemPromptFile != "" {
		path, err := filepath.Abs(opts.SystemPromptFile)
		if err != nil {
			return "", fmt.Errorf("resolve claude system prompt file %s: %w", opts.SystemPromptFile, err)
		}
		return path, nil
	}
	dir := opts.AttemptDir
	if dir == "" {
		return "", fmt.Errorf("attempt dir is required to materialize the Windows Claude system prompt")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create claude system prompt dir %s: %w", dir, err)
	}
	path := filepath.Join(dir, ClaudeSystemPromptFilename)
	if err := os.WriteFile(path, []byte(opts.SystemPrompt), 0o600); err != nil {
		return "", fmt.Errorf("write claude system prompt file %s: %w", path, err)
	}
	return path, nil
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
