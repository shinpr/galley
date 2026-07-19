package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDecodeSupervisorVerdictClassifiesContractErrors(t *testing.T) {
	tests := []struct {
		name   string
		output string
	}{
		{name: "invalid JSON", output: "not json"},
		{name: "invalid verdict", output: `{"status":"needs_revision"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := decodeSupervisorVerdict("claude", []byte(tt.output), Evidence{})
			if err == nil {
				t.Fatal("expected supervisor verdict contract error")
			}
			var contractErr *VerdictContractError
			if !errors.As(err, &contractErr) {
				t.Fatalf("error type got %T, want *VerdictContractError: %v", err, err)
			}
		})
	}
}

func TestExtractJSONObjectStripsSurroundingProse(t *testing.T) {
	cases := map[string]string{
		"Here is the verdict:\n{\"status\":\"accepted\"}\nDone.": `{"status":"accepted"}`,
		`{"status":"accepted"}`:                                  `{"status":"accepted"}`,
		"no json here":                                           "no json here",
	}
	for in, want := range cases {
		if got := string(extractJSONObject([]byte(in))); got != want {
			t.Fatalf("extractJSONObject(%q)=%q, want %q", in, got, want)
		}
	}
}

func TestAppendSupervisorModel(t *testing.T) {
	base := []string{"supervisor"}
	if got := appendSupervisorModel(base, ""); len(got) != 1 {
		t.Fatalf("empty model changed argv: %v", got)
	}
	got := appendSupervisorModel(base, "provider-model-x")
	if strings.Join(got, " ") != "supervisor --model provider-model-x" {
		t.Fatalf("configured model argv got %v", got)
	}
}

func TestRunAdapterPayloadCodexUsesEmbeddedPromptAndSchema(t *testing.T) {
	skipPOSIXFakeSupervisorOnWindows(t)
	binDir := t.TempDir()
	fakeCodex := filepath.Join(binDir, "codex")
	capturePath := filepath.Join(t.TempDir(), "codex.args")
	if err := os.WriteFile(fakeCodex, []byte(`#!/bin/sh
printf '%s\n' "$*" > `+capturePath+`
out=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --output-last-message)
      out="$2"
      shift 2
      ;;
    *)
      shift
      ;;
  esac
done
cat >/dev/null
printf '%s\n' '{"status":"accepted","summary":"ok","acceptance_passes":["AC1"],"quality_passes":[],"findings":[],"discussion_items":[]}' > "$out"
printf '%s\n' '{"event":"done"}'
`), 0o700); err != nil {
		t.Fatal(err)
	}
	artifactDir := t.TempDir()

	output, err := RunAdapterPayload(context.Background(), AdapterOptions{
		Provider:    "codex",
		Model:       "provider-model-x",
		WorkDir:     t.TempDir(),
		ArtifactDir: artifactDir,
		CodexBin:    fakeCodex,
	}, []byte(`{"evidence":{"task":{"id":"task"},"diff":""}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(output), `"status":"accepted"`) {
		t.Fatalf("output got %q", output)
	}
	prompt, err := os.ReadFile(filepath.Join(artifactDir, "codex_supervisor_prompt.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"You are the Galley supervisor", "Evidence JSON", "supervisor verdict schema"} {
		if !strings.Contains(string(prompt), want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
	args, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"exec", "--sandbox", "workspace-write", "--output-schema", "--output-last-message"} {
		if !strings.Contains(string(args), want) {
			t.Fatalf("codex args missing %q:\n%s", want, args)
		}
	}
	if strings.Count(string(args), "--model provider-model-x") != 1 {
		t.Fatalf("codex args must contain one configured model: %s", args)
	}
	schema, err := os.ReadFile(filepath.Join(artifactDir, "supervisor-verdict.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(schema), `"acceptance_passes"`) || !strings.Contains(string(schema), `"quality_passes"`) {
		t.Fatalf("Codex supervisor schema lost quality results: %s", schema)
	}
	var converted map[string]any
	if err := json.Unmarshal(schema, &converted); err != nil {
		t.Fatalf("decode Codex supervisor schema: %v", err)
	}
	required, _ := converted["required"].([]any)
	if len(required) != len(converted["properties"].(map[string]any)) {
		t.Fatalf("Codex supervisor schema did not require every property: %s", schema)
	}
	if strings.Contains(string(schema), `"uniqueItems"`) {
		t.Fatalf("Codex supervisor schema retained unsupported uniqueItems: %s", schema)
	}
}

func TestRunAdapterPayloadGrokUsesEnvelopeAndReadOnlySandbox(t *testing.T) {
	skipPOSIXFakeSupervisorOnWindows(t)
	bin := filepath.Join(t.TempDir(), "grok")
	argsPath := filepath.Join(t.TempDir(), "args")
	verdict := `{"status":"accepted","summary":"ok","acceptance_passes":["AC1"],"quality_passes":[],"findings":[],"discussion_items":[]}`
	script := "#!/bin/sh\nprintf '%s' \"$*\" > '" + argsPath + "'\nprintf '%s' '" + `{"text":` + shellJSONQuote(verdict) + `,"stopReason":"EndTurn","sessionId":"s"}` + "'\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	output, err := RunAdapterPayload(context.Background(), AdapterOptions{Provider: "grok", WorkDir: t.TempDir(), ArtifactDir: t.TempDir(), GrokBin: bin, Effort: "high"}, []byte(`{"evidence":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(output), `"status":"accepted"`) {
		t.Fatalf("output = %s", output)
	}
	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--permission-mode bypassPermissions", "--sandbox read-only"} {
		if !strings.Contains(string(args), want) {
			t.Fatalf("Grok supervisor args missing %q: %s", want, args)
		}
	}
}

func TestRunAdapterPayloadGrokPersistsNonEndTurnMetadata(t *testing.T) {
	skipPOSIXFakeSupervisorOnWindows(t)
	bin := filepath.Join(t.TempDir(), "grok")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nprintf '%s\\n' '{\"text\":\"{}\",\"stopReason\":\"Cancelled\",\"sessionId\":\"cancelled-session\"}'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	artifacts := t.TempDir()
	_, err := RunAdapterPayload(context.Background(), AdapterOptions{Provider: "grok", WorkDir: t.TempDir(), ArtifactDir: artifacts, GrokBin: bin}, []byte(`{"evidence":{}}`))
	if err == nil || !strings.Contains(err.Error(), "Cancelled") {
		t.Fatalf("error = %v", err)
	}
	metadata, readErr := os.ReadFile(filepath.Join(artifacts, "grok_supervisor_completion.json"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(metadata), "cancelled-session") {
		t.Fatalf("metadata = %s", metadata)
	}
}

func shellJSONQuote(text string) string {
	b, _ := json.Marshal(text)
	return string(b)
}

func TestRunAdapterPayloadClaudeUsesEmbeddedPromptAndSchema(t *testing.T) {
	skipPOSIXFakeSupervisorOnWindows(t)
	binDir := t.TempDir()
	capturePath := filepath.Join(t.TempDir(), "claude.args")
	envPath := filepath.Join(t.TempDir(), "claude.env")
	fakeClaude := filepath.Join(binDir, "claude")
	if err := os.WriteFile(fakeClaude, []byte(`#!/bin/sh
printf '%s\n' "$*" > `+capturePath+`
printf '%s\n' "$GALLEY_CLAUDE_GUARD_MODE" > `+envPath+`
cat >/dev/null
printf '%s\n' '{"status":"accepted","summary":"ok","acceptance_passes":["AC1"],"quality_passes":[],"findings":[],"discussion_items":[]}'
`), 0o700); err != nil {
		t.Fatal(err)
	}

	output, err := RunAdapterPayload(context.Background(), AdapterOptions{
		Provider:    "claude",
		Model:       "provider-model-x",
		WorkDir:     t.TempDir(),
		ArtifactDir: t.TempDir(),
		ClaudeBin:   fakeClaude,
	}, []byte(`{"evidence":{"task":{"id":"task"},"diff":""}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(output), `"status":"accepted"`) {
		t.Fatalf("output got %q", output)
	}
	args, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--system-prompt", "--json-schema", "--plugin-dir", "--permission-mode", "bypassPermissions", "--tools", "default", "--disallowedTools", "Write,Edit,MultiEdit,NotebookEdit", "Galley Supervisor Verdict"} {
		if !strings.Contains(string(args), want) {
			t.Fatalf("claude args missing %q:\n%s", want, args)
		}
	}
	if strings.Count(string(args), "--model provider-model-x") != 1 {
		t.Fatalf("claude args must contain one configured model: %s", args)
	}
	if strings.Contains(string(args), "$schema") || strings.Contains(string(args), "draft/2020-12") {
		t.Fatalf("claude supervisor schema must omit root $schema:\n%s", args)
	}
	for _, unwanted := range []string{"--allowedTools", "--allowed-tools"} {
		if strings.Contains(string(args), unwanted) {
			t.Fatalf("claude args should not set %q:\n%s", unwanted, args)
		}
	}
	env, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(env)) != "supervisor" {
		t.Fatalf("guard mode env got %q, want supervisor", env)
	}
}

func TestRunAdapterPayloadClaudeReadsFullStdoutVerdict(t *testing.T) {
	skipPOSIXFakeSupervisorOnWindows(t)
	binDir := t.TempDir()
	fakeClaude := filepath.Join(binDir, "claude")
	longSummary := strings.Repeat("x", 70*1024)
	if err := os.WriteFile(fakeClaude, []byte(`#!/bin/sh
cat >/dev/null
printf '%s' '{"status":"accepted","summary":"`+longSummary+`","acceptance_passes":["AC1"],"quality_passes":[],"findings":[],"discussion_items":[]}'
`), 0o700); err != nil {
		t.Fatal(err)
	}

	output, err := RunAdapterPayload(context.Background(), AdapterOptions{
		Provider:    "claude",
		WorkDir:     t.TempDir(),
		ArtifactDir: t.TempDir(),
		ClaudeBin:   fakeClaude,
	}, []byte(`{"evidence":{"task":{"id":"task"},"diff":""}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(output), longSummary) {
		t.Fatal("large supervisor verdict was truncated")
	}
}

func skipPOSIXFakeSupervisorOnWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("test uses POSIX shell fake supervisor binaries")
	}
}
