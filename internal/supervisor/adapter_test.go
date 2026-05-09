package supervisor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunAdapterPayloadCodexUsesEmbeddedPromptAndSchema(t *testing.T) {
	binDir := t.TempDir()
	fakeCodex := filepath.Join(binDir, "codex")
	if err := os.WriteFile(fakeCodex, []byte(`#!/bin/sh
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
printf '%s\n' '{"status":"accepted","summary":"ok","acceptance_gaps":[],"reviewed_files":["README.md"],"acceptance_evidence":[{"ac_id":"AC1","evidence":["checked"]}],"findings":[],"residual_risks":[],"confidence":"medium","next_work_order":""}' > "$out"
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
	if _, err := os.Stat(filepath.Join(artifactDir, "supervisor-verdict.schema.json")); err != nil {
		t.Fatal(err)
	}
}

func TestRunAdapterPayloadClaudeUsesEmbeddedPromptAndSchema(t *testing.T) {
	binDir := t.TempDir()
	capturePath := filepath.Join(t.TempDir(), "claude.args")
	fakeClaude := filepath.Join(binDir, "claude")
	if err := os.WriteFile(fakeClaude, []byte(`#!/bin/sh
printf '%s\n' "$*" > `+capturePath+`
cat >/dev/null
printf '%s\n' '{"status":"accepted","summary":"ok","acceptance_gaps":[],"reviewed_files":["README.md"],"acceptance_evidence":[{"ac_id":"AC1","evidence":["checked"]}],"findings":[],"residual_risks":[],"confidence":"medium","next_work_order":""}'
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
	for _, want := range []string{"--system-prompt", "--json-schema", "--allowedTools", "Galley Supervisor Verdict"} {
		if !strings.Contains(string(args), want) {
			t.Fatalf("claude args missing %q:\n%s", want, args)
		}
	}
}

func TestRunAdapterPayloadClaudeReadsFullStdoutVerdict(t *testing.T) {
	binDir := t.TempDir()
	fakeClaude := filepath.Join(binDir, "claude")
	longSummary := strings.Repeat("x", 70*1024)
	if err := os.WriteFile(fakeClaude, []byte(`#!/bin/sh
cat >/dev/null
printf '%s' '{"status":"accepted","summary":"`+longSummary+`","acceptance_gaps":[],"reviewed_files":["README.md"],"acceptance_evidence":[{"ac_id":"AC1","evidence":["checked"]}],"findings":[],"residual_risks":[],"confidence":"medium","next_work_order":""}'
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
