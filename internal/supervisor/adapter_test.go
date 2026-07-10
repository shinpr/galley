package supervisor

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

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
printf '%s\n' '{"status":"accepted","summary":"ok","acceptance_gaps":[],"reviewed_files":["README.md"],"acceptance_evidence":[{"ac_id":"AC1","evidence":["checked"]}],"findings":[],"residual_risks":[],"discussion_items":[],"confidence":"medium","next_work_order":""}' > "$out"
printf '%s\n' '{"event":"done"}'
`), 0o700); err != nil {
		t.Fatal(err)
	}
	artifactDir := t.TempDir()

	output, err := RunAdapterPayload(context.Background(), AdapterOptions{
		Provider:    "codex",
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
	if _, err := os.Stat(filepath.Join(artifactDir, "supervisor-verdict.schema.json")); err != nil {
		t.Fatal(err)
	}
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
printf '%s\n' '{"status":"accepted","summary":"ok","acceptance_gaps":[],"reviewed_files":["README.md"],"acceptance_evidence":[{"ac_id":"AC1","evidence":["checked"]}],"findings":[],"residual_risks":[],"discussion_items":[],"confidence":"medium","next_work_order":""}'
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
printf '%s' '{"status":"accepted","summary":"`+longSummary+`","acceptance_gaps":[],"reviewed_files":["README.md"],"acceptance_evidence":[{"ac_id":"AC1","evidence":["checked"]}],"findings":[],"residual_risks":[],"discussion_items":[],"confidence":"medium","next_work_order":""}'
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
