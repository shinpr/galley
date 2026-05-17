package runner

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/shinpr/galley/internal/task"
)

func TestClaudeArgvReplacePrompt(t *testing.T) {
	t.Parallel()
	promptPath, schemaPath := writePromptFixtures(t)

	argv, err := ClaudeArgv(ClaudeOptions{
		Model:            "opus",
		Effort:           "high",
		PromptMode:       "replace",
		PermissionMode:   "acceptEdits",
		SystemPromptFile: promptPath,
		JSONSchemaFile:   schemaPath,
		MaxBudgetUSD:     5,
		PluginDirs:       []string{"/tmp/galley-guard"},
		Prompt:           "do the work",
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		"claude", "-p", "--output-format", "stream-json", "--verbose",
		"--model", "opus",
		"--effort", "high",
		"--permission-mode", "acceptEdits",
		"--system-prompt", "system prompt",
		"--json-schema", `{"type":"object"}`,
		"--plugin-dir", "/tmp/galley-guard",
		"--max-budget-usd", "5",
		"do the work",
	}
	if !reflect.DeepEqual(argv, want) {
		t.Fatalf("argv mismatch\n got: %#v\nwant: %#v", argv, want)
	}
}

func TestClaudeArgvAppendPrompt(t *testing.T) {
	t.Parallel()
	promptPath, _ := writePromptFixtures(t)

	argv, err := ClaudeArgv(ClaudeOptions{
		PromptMode:       "append",
		SystemPromptFile: promptPath,
		JSONSchema:       `{"type":"object"}`,
		Prompt:           "do the work",
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		"claude", "-p", "--output-format", "stream-json", "--verbose",
		"--append-system-prompt", "system prompt",
		"--json-schema", `{"type":"object"}`,
		"do the work",
	}
	if !reflect.DeepEqual(argv, want) {
		t.Fatalf("argv mismatch\n got: %#v\nwant: %#v", argv, want)
	}
}

func TestClaudeCommandPlanIncludesWorkDirAndWarnings(t *testing.T) {
	t.Parallel()

	command, err := ClaudeCommandPlan(ClaudeOptions{
		WorkDir:        "/tmp/project",
		PermissionMode: "bypassPermissions",
		Prompt:         "do the work",
	})
	if err != nil {
		t.Fatal(err)
	}

	if command.WorkDir != "/tmp/project" {
		t.Fatalf("work dir mismatch: %q", command.WorkDir)
	}
	if len(command.Warnings) != 1 {
		t.Fatalf("expected 1 warning, got %#v", command.Warnings)
	}
}

func TestClaudeCommandPlanUsesEmbeddedPromptAndSchemaByDefault(t *testing.T) {
	t.Parallel()

	command, err := ClaudeCommandPlan(ClaudeOptions{
		Prompt: "do the work",
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(command.Argv, "\x00")
	for _, want := range []string{"--system-prompt", "Galley Claude Executor", "--json-schema", "Galley Claude Executor Result"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("argv missing %q", want)
		}
	}
}

func TestClaudeFromTaskUsesExplicitExecutorBudget(t *testing.T) {
	t.Parallel()
	budget := 6.25
	tk := task.Task{
		Scope: task.Scope{
			CWD:        "/tmp/project",
			Permission: "edit",
		},
		Executor: task.Executor{
			CLI:           "claude",
			Effort:        "high",
			PromptProfile: "codexized-claude-executor-v1",
			PromptMode:    "replace",
			MaxBudgetUSD:  &budget,
		},
	}
	opts := FromTask(tk)
	opts.SystemPrompt = "system"
	opts.JSONSchema = `{"type":"object"}`
	opts.Prompt = "do the work"

	argv, err := ClaudeArgv(opts)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < len(argv)-1; i++ {
		if argv[i] == "--max-budget-usd" && argv[i+1] == "6.25" {
			return
		}
	}
	t.Fatalf("explicit task budget did not reach Claude argv: %#v", argv)
}

func TestClaudeArgvRejectsUnknownPromptMode(t *testing.T) {
	t.Parallel()
	promptPath, _ := writePromptFixtures(t)

	_, err := ClaudeArgv(ClaudeOptions{
		PromptMode:       "bad",
		SystemPromptFile: promptPath,
		Prompt:           "do the work",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "prompt mode") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestClaudeShellPreviewUsesCatAndCd(t *testing.T) {
	t.Parallel()
	promptPath, err := filepath.Abs("prompts/claude-executor-full.md")
	if err != nil {
		t.Fatal(err)
	}
	schemaPath, err := filepath.Abs("schemas/claude-result.schema.json")
	if err != nil {
		t.Fatal(err)
	}

	got, warnings, err := ClaudeShellPreview(ClaudeOptions{
		WorkDir:          "/tmp/project",
		PromptMode:       "replace",
		SystemPromptFile: "prompts/claude-executor-full.md",
		JSONSchemaFile:   "schemas/claude-result.schema.json",
		Prompt:           "do the work",
	})
	if err != nil {
		t.Fatal(err)
	}

	want := `cd /tmp/project && claude -p --output-format stream-json --verbose --system-prompt "$(cat ` + shellToken(promptPath) + `)" --json-schema "$(cat ` + shellToken(schemaPath) + `)" 'do the work'`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %#v", warnings)
	}
}

func TestFromTaskMapsPermissionAndDefaultsPromptMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		permission string
		promptMode string
		wantPerm   string
		wantPrompt string
	}{
		{name: "read only", permission: "read-only", wantPerm: "plan", wantPrompt: "replace"},
		{name: "edit", permission: "edit", wantPerm: "acceptEdits", wantPrompt: "replace"},
		{name: "sandbox full access", permission: "sandbox-full-access", wantPerm: "bypassPermissions", wantPrompt: "replace"},
		{name: "append", permission: "edit", promptMode: "append", wantPerm: "acceptEdits", wantPrompt: "append"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			opts := FromTask(task.Task{
				Scope: task.Scope{CWD: "/tmp/project", Permission: tt.permission},
				Executor: task.Executor{
					Model:      "opus",
					Effort:     "high",
					PromptMode: tt.promptMode,
				},
			})
			if opts.PermissionMode != tt.wantPerm {
				t.Fatalf("permission mode got %q, want %q", opts.PermissionMode, tt.wantPerm)
			}
			if opts.PromptMode != tt.wantPrompt {
				t.Fatalf("prompt mode got %q, want %q", opts.PromptMode, tt.wantPrompt)
			}
			if opts.WorkDir != "/tmp/project" {
				t.Fatalf("work dir got %q", opts.WorkDir)
			}
		})
	}
}

// TestClaudeCommandPlanWindowsRoutesPromptThroughStdinAndFile pins the
// Windows command-line length fix for AC1, AC2, and AC3:
//   - The Galley-generated system prompt body is materialized to a file and
//     referenced via --system-prompt-file, not embedded in argv.
//   - The work order prompt is delivered through stdin, not appended to argv.
//   - The JSON schema body is intentionally not passed on argv.
//   - Total argv byte length stays well below the Windows command-line limit.
func TestClaudeCommandPlanWindowsRoutesPromptThroughStdinAndFile(t *testing.T) {
	t.Parallel()
	attemptDir := t.TempDir()
	systemPrompt := strings.Repeat("system-prompt-body ", 4096)
	jsonSchema := strings.Repeat("{\"x\":\"y\"} ", 4096)
	workOrder := strings.Repeat("work-order-line\n", 4096)

	command, err := ClaudeCommandPlanForOS(ClaudeOptions{
		Bin:            "claude",
		Model:          "opus",
		PermissionMode: "acceptEdits",
		PromptMode:     "replace",
		SystemPrompt:   systemPrompt,
		JSONSchema:     jsonSchema,
		Prompt:         workOrder,
		AttemptDir:     attemptDir,
		MaxBudgetUSD:   5,
	}, "windows")
	if err != nil {
		t.Fatal(err)
	}

	joined := strings.Join(command.Argv, "\x00")
	if strings.Contains(joined, systemPrompt) {
		t.Fatal("Windows argv must not contain the system prompt body")
	}
	if strings.Contains(joined, jsonSchema) {
		t.Fatal("Windows argv must not contain the JSON schema body")
	}
	if strings.Contains(joined, workOrder) {
		t.Fatal("Windows argv must not contain the work order prompt body")
	}
	if !containsArg(command.Argv, "--system-prompt-file") {
		t.Fatalf("Windows argv must use --system-prompt-file: %#v", command.Argv)
	}
	if containsArg(command.Argv, "--json-schema") {
		t.Fatalf("Windows argv must omit --json-schema: %#v", command.Argv)
	}
	if containsArg(command.Argv, "--system-prompt") {
		t.Fatalf("Windows argv must not use bare --system-prompt: %#v", command.Argv)
	}
	if command.Stdin != workOrder {
		t.Fatal("Windows stdin must equal the work order prompt body")
	}
	total := 0
	for _, a := range command.Argv {
		total += len(a) + 1
	}
	if total > 8000 {
		t.Fatalf("Windows argv length %d exceeds Windows-safe threshold", total)
	}
	body, err := os.ReadFile(filepath.Join(attemptDir, ClaudeSystemPromptFilename))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != systemPrompt {
		t.Fatal("materialized system prompt file body does not match opts.SystemPrompt")
	}
}

// TestClaudeCommandPlanWindowsAppendModeUsesAppendSystemPromptFile pins the
// Windows append-mode routing for AC2: append prompt mode also delivers the
// system prompt through a file flag, not on argv.
func TestClaudeCommandPlanWindowsAppendModeUsesAppendSystemPromptFile(t *testing.T) {
	t.Parallel()
	attemptDir := t.TempDir()
	systemPrompt := "extension prompt body"

	command, err := ClaudeCommandPlanForOS(ClaudeOptions{
		Bin:          "claude",
		PromptMode:   "append",
		SystemPrompt: systemPrompt,
		JSONSchema:   `{"type":"object"}`,
		Prompt:       "do the work",
		AttemptDir:   attemptDir,
	}, "windows")
	if err != nil {
		t.Fatal(err)
	}

	if !containsArg(command.Argv, "--append-system-prompt-file") {
		t.Fatalf("append-mode Windows argv must use --append-system-prompt-file: %#v", command.Argv)
	}
	if containsArg(command.Argv, "--append-system-prompt") && !containsArg(command.Argv, "--append-system-prompt-file") {
		t.Fatalf("append-mode Windows argv must not use bare --append-system-prompt: %#v", command.Argv)
	}
	if command.Stdin != "do the work" {
		t.Fatalf("append-mode Windows stdin should equal work order, got %q", command.Stdin)
	}
}

// TestClaudeCommandPlanNonWindowsPreservesArgvShape pins AC4: on macOS/Linux
// the Claude command plan keeps the historical argv shape so existing field
// evidence continues to apply.
func TestClaudeCommandPlanNonWindowsPreservesArgvShape(t *testing.T) {
	t.Parallel()
	command, err := ClaudeCommandPlanForOS(ClaudeOptions{
		Bin:          "claude",
		PromptMode:   "replace",
		SystemPrompt: "system body",
		JSONSchema:   `{"type":"object"}`,
		Prompt:       "do the work",
	}, "linux")
	if err != nil {
		t.Fatal(err)
	}

	if !containsArg(command.Argv, "--system-prompt") {
		t.Fatalf("non-Windows argv must keep --system-prompt: %#v", command.Argv)
	}
	if !containsArg(command.Argv, "--json-schema") {
		t.Fatalf("non-Windows argv must keep --json-schema: %#v", command.Argv)
	}
	if command.Argv[len(command.Argv)-1] != "do the work" {
		t.Fatalf("non-Windows argv must end with work order prompt: %#v", command.Argv)
	}
	if command.Stdin != "" {
		t.Fatalf("non-Windows command should not set stdin, got %q", command.Stdin)
	}
}

func containsArg(argv []string, flag string) bool {
	for _, a := range argv {
		if a == flag {
			return true
		}
	}
	return false
}

func writePromptFixtures(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	promptPath := filepath.Join(dir, "prompt.md")
	schemaPath := filepath.Join(dir, "schema.json")
	if err := os.WriteFile(promptPath, []byte("system prompt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(schemaPath, []byte(`{"type":"object"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return promptPath, schemaPath
}
