package supervisor

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// This test file compiles on every OS and passes an explicit goos value into
// runClaudeAdapterForOS; tests that execute POSIX fake binaries skip on Windows.

// TestRunClaudeAdapterWindowsRoutesSystemPromptToFile pins the Windows
// supervisor command shape for AC1, AC2, and AC3:
//   - The supervisor system prompt body is materialized into a real file
//     under the artifact directory and referenced via --system-prompt-file.
//   - The JSON schema body is intentionally not passed on argv; verdict
//     validators still reject malformed final output.
//   - The serialized supervisor request stays on stdin.
//
// The actual subprocess is skipped on Windows-only build environments because
// this test compiles a POSIX shell fake claude binary; on Windows the
// equivalent assertions are exercised through the cross-platform
// `GOOS=windows go test -exec=true` compile check.
func TestRunClaudeAdapterWindowsRoutesSystemPromptToFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fake claude is unavailable; Windows behavior is covered by GOOS=windows go test -exec=true compile evidence")
	}
	binDir := t.TempDir()
	artifactDir := t.TempDir()
	capturePath := filepath.Join(t.TempDir(), "claude.args")
	stdinPath := filepath.Join(t.TempDir(), "claude.stdin")
	systemPromptCapturePath := filepath.Join(t.TempDir(), "claude.system-prompt")
	fakeClaude := filepath.Join(binDir, "claude")
	if err := os.WriteFile(fakeClaude, []byte(`#!/bin/sh
printf '%s\n' "$*" > `+capturePath+`
while [ "$#" -gt 0 ]; do
  if [ "$1" = "--system-prompt-file" ]; then
    shift
    cat "$1" > `+systemPromptCapturePath+`
    break
  fi
  shift
done
cat > `+stdinPath+`
printf '%s\n' '{"status":"accepted","summary":"ok","acceptance_gaps":[],"reviewed_files":["README.md"],"acceptance_evidence":[{"ac_id":"AC1","evidence":["checked"]}],"findings":[],"residual_risks":[],"discussion_items":[],"confidence":"medium","next_work_order":""}'
`), 0o700); err != nil {
		t.Fatal(err)
	}

	output, err := runClaudeAdapterForOS(context.Background(), AdapterOptions{
		Provider:    "claude",
		Model:       "provider-model-x",
		WorkDir:     t.TempDir(),
		ArtifactDir: artifactDir,
		ClaudeBin:   fakeClaude,
	}, []byte(`{"evidence":{"task":{"id":"task"},"diff":""}}`), "windows")
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
	argsStr := string(args)
	if !strings.Contains(argsStr, "--system-prompt-file") {
		t.Fatalf("Windows supervisor argv must use --system-prompt-file: %s", argsStr)
	}
	if strings.Contains(argsStr, "--json-schema") {
		t.Fatalf("Windows supervisor argv must not include --json-schema: %s", argsStr)
	}
	if strings.Count(argsStr, "--model provider-model-x") != 1 {
		t.Fatalf("Windows supervisor argv must contain one configured model: %s", argsStr)
	}
	// The bare --system-prompt flag must not appear; only --system-prompt-file.
	if strings.Contains(argsStr, "--system-prompt ") || strings.HasSuffix(argsStr, "--system-prompt") {
		t.Fatalf("Windows supervisor argv must not include bare --system-prompt: %s", argsStr)
	}

	// The materialized supervisor system prompt file must exist alongside
	// other artifacts so per-attempt evidence is preserved.
	if _, err := os.Stat(filepath.Join(artifactDir, ClaudeSupervisorSystemPromptFilename)); err != nil {
		t.Fatalf("Windows supervisor system prompt file missing: %v", err)
	}
	systemPrompt, err := os.ReadFile(systemPromptCapturePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(systemPrompt), "Galley Supervisor Contract") {
		t.Fatalf("fake Claude did not receive readable supervisor system prompt content: %q", systemPrompt)
	}

	// The serialized request must still be delivered via stdin.
	stdin, err := os.ReadFile(stdinPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(stdin), `"evidence"`) {
		t.Fatalf("Windows supervisor stdin must carry the request: %s", stdin)
	}
}
