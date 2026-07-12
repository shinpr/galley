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

	command, err := ClaudeCommandPlanForOS(ClaudeOptions{
		Model:            "opus",
		Effort:           "high",
		PromptMode:       "replace",
		PermissionMode:   "acceptEdits",
		SystemPromptFile: promptPath,
		JSONSchemaFile:   schemaPath,
		PluginDirs:       []string{"/tmp/galley-guard"},
		Prompt:           "do the work",
	}, "linux")
	if err != nil {
		t.Fatal(err)
	}
	argv := command.Argv

	want := []string{
		"claude", "-p", "--output-format", "stream-json", "--verbose",
		"--model", "opus",
		"--effort", "high",
		"--permission-mode", "acceptEdits",
		"--system-prompt", "system prompt",
		"--json-schema", `{"type":"object"}`,
		"--plugin-dir", "/tmp/galley-guard",
		"do the work",
	}
	if !reflect.DeepEqual(argv, want) {
		t.Fatalf("argv mismatch\n got: %#v\nwant: %#v", argv, want)
	}
}

func TestClaudeArgvAppendPrompt(t *testing.T) {
	t.Parallel()
	promptPath, _ := writePromptFixtures(t)

	command, err := ClaudeCommandPlanForOS(ClaudeOptions{
		PromptMode:       "append",
		SystemPromptFile: promptPath,
		JSONSchema:       `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object"}`,
		Prompt:           "do the work",
	}, "linux")
	if err != nil {
		t.Fatal(err)
	}
	argv := command.Argv

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

	command, err := ClaudeCommandPlanForOS(ClaudeOptions{
		WorkDir:        "/tmp/project",
		PermissionMode: "bypassPermissions",
		Prompt:         "do the work",
	}, "linux")
	if err != nil {
		t.Fatal(err)
	}

	if command.WorkDir != "/tmp/project" {
		t.Fatalf("work dir mismatch: %q", command.WorkDir)
	}
	joinedWarnings := strings.Join(command.Warnings, "\n")
	if !strings.Contains(joinedWarnings, "bypassPermissions") || !strings.Contains(joinedWarnings, "isolated sandbox/worktree") {
		t.Fatalf("expected bypassPermissions sandbox/worktree safety warning, got %#v", command.Warnings)
	}
}

func TestClaudeCommandPlanUsesEmbeddedPromptAndSchemaByDefault(t *testing.T) {
	t.Parallel()

	command, err := ClaudeCommandPlanForOS(ClaudeOptions{
		Prompt: "do the work",
	}, "linux")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(command.Argv, "\x00")
	for _, want := range []string{"--system-prompt", "Galley Claude Executor", "--json-schema", "Galley Claude Executor Result"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("argv missing %q", want)
		}
	}
	if strings.Contains(argAfter(t, command.Argv, "--json-schema"), "$schema") {
		t.Fatalf("Claude argv schema must omit root $schema: %s", argAfter(t, command.Argv, "--json-schema"))
	}
	if strings.Contains(argAfter(t, command.Argv, "--json-schema"), `"allOf"`) {
		t.Fatalf("Claude argv schema must omit root allOf: %s", argAfter(t, command.Argv, "--json-schema"))
	}
}

func TestClaudeArgvRejectsUnknownPromptMode(t *testing.T) {
	t.Parallel()
	promptPath, _ := writePromptFixtures(t)

	_, err := ClaudeCommandPlanForOS(ClaudeOptions{
		PromptMode:       "bad",
		SystemPromptFile: promptPath,
		Prompt:           "do the work",
	}, "linux")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "prompt mode") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestClaudeShellPreviewUsesCatAndCd(t *testing.T) {
	t.Parallel()
	promptPath, schemaPath := writePromptFixtures(t)

	got, warnings, err := ClaudeShellPreview(ClaudeOptions{
		WorkDir:          "/tmp/project",
		PromptMode:       "replace",
		SystemPromptFile: promptPath,
		JSONSchemaFile:   schemaPath,
		Prompt:           "do the work",
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		`cd /tmp/project && claude -p --output-format stream-json --verbose`,
		`--system-prompt "$(cat ` + shellToken(promptPath) + `)"`,
		`--json-schema`,
		`'do the work'`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("preview missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "$schema") || strings.Contains(got, `$(cat `+shellToken(schemaPath)+`)`) {
		t.Fatalf("preview must inline Claude-compatible schema without root $schema:\n%s", got)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %#v", warnings)
	}
}

func TestFromTaskMapsPermissionAndOwnsPromptMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		permission string
		wantPerm   string
	}{
		{name: "read only", permission: "read-only", wantPerm: "plan"},
		{name: "edit", permission: "edit", wantPerm: "acceptEdits"},
		{name: "sandbox full access", permission: "sandbox-full-access", wantPerm: "bypassPermissions"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			opts := FromTask(task.Task{
				Scope: task.Scope{CWD: "/tmp/project", Permission: tt.permission},
				Executor: task.Executor{
					Model:  "opus",
					Effort: "high",
				},
			})
			if opts.PermissionMode != tt.wantPerm {
				t.Fatalf("permission mode got %q, want %q", opts.PermissionMode, tt.wantPerm)
			}
			// Galley owns prompt transport; task YAML cannot select append/replace.
			if opts.PromptMode != galleyPromptMode {
				t.Fatalf("prompt mode got %q, want %q", opts.PromptMode, galleyPromptMode)
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

func TestClaudeCommandPlanWindowsResolvesSystemPromptFileBeforeSubprocessCWD(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "prompt.md"), []byte("system prompt"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	command, err := ClaudeCommandPlanForOS(ClaudeOptions{
		Bin:              "claude",
		PromptMode:       "replace",
		SystemPromptFile: "prompt.md",
		JSONSchema:       `{"type":"object"}`,
		Prompt:           "do the work",
		WorkDir:          filepath.Join(dir, "child-workdir"),
	}, "windows")
	if err != nil {
		t.Fatal(err)
	}

	path := argAfter(t, command.Argv, "--system-prompt-file")
	if !filepath.IsAbs(path) {
		t.Fatalf("Windows system prompt file path must be absolute, got %q", path)
	}
	if path != filepath.Join(dir, "prompt.md") {
		t.Fatalf("Windows system prompt file path got %q, want %q", path, filepath.Join(dir, "prompt.md"))
	}
}

func TestClaudeCommandPlanWindowsRequiresAttemptDirForEmbeddedSystemPrompt(t *testing.T) {
	t.Parallel()
	_, err := ClaudeCommandPlanForOS(ClaudeOptions{
		Bin:          "claude",
		PromptMode:   "replace",
		SystemPrompt: "embedded system prompt",
		JSONSchema:   `{"type":"object"}`,
		Prompt:       "do the work",
	}, "windows")
	if err == nil {
		t.Fatal("expected an error when Windows embedded system prompt has no AttemptDir")
	}
	if !strings.Contains(err.Error(), "attempt dir is required") {
		t.Fatalf("unexpected error: %v", err)
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
		JSONSchema:   `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object"}`,
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
	if got := argAfter(t, command.Argv, "--json-schema"); got != `{"type":"object"}` {
		t.Fatalf("non-Windows argv must pass Claude-compatible schema, got %q", got)
	}
	if command.Argv[len(command.Argv)-1] != "do the work" {
		t.Fatalf("non-Windows argv must end with work order prompt: %#v", command.Argv)
	}
	if command.Stdin != "" {
		t.Fatalf("non-Windows command should not set stdin, got %q", command.Stdin)
	}
}

func TestClaudeCompatibleJSONSchemaRemovesUnsupportedRootKeywordsOnly(t *testing.T) {
	t.Parallel()
	got, err := ClaudeCompatibleJSONSchema(`{"$schema":"https://json-schema.org/draft/2020-12/schema","allOf":[{"required":["x"]}],"anyOf":[{"type":"object"}],"oneOf":[{"type":"object"}],"type":"object","properties":{"nested":{"$schema":"kept","allOf":[{"required":["y"]}],"type":"string"}}}`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "draft/2020-12") {
		t.Fatalf("root $schema was not removed: %s", got)
	}
	for _, unsupported := range []string{`"allOf":[{"required":["x"]}]`, `"anyOf":[{"type":"object"}]`, `"oneOf":[{"type":"object"}]`} {
		if strings.Contains(got, unsupported) {
			t.Fatalf("root unsupported keyword was not removed: %s", got)
		}
	}
	if !strings.Contains(got, `"nested":{"$schema":"kept","allOf":[{"required":["y"]}],"type":"string"}`) {
		t.Fatalf("nested schema metadata should remain untouched: %s", got)
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

func argAfter(t *testing.T, argv []string, flag string) string {
	t.Helper()
	for i := 0; i < len(argv)-1; i++ {
		if argv[i] == flag {
			return argv[i+1]
		}
	}
	t.Fatalf("argv missing %s: %#v", flag, argv)
	return ""
}

func writePromptFixtures(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	promptPath := filepath.Join(dir, "prompt.md")
	schemaPath := filepath.Join(dir, "schema.json")
	if err := os.WriteFile(promptPath, []byte("system prompt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(schemaPath, []byte(`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return promptPath, schemaPath
}
