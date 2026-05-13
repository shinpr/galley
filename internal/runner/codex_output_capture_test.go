package runner

// AC: AC4 — The Codex executor command plan must wire the structured
// executor-result schema through `codex exec --output-schema` and request a
// `--output-last-message` capture file, and the captured final message must
// parse back into a ClaudeResult so completed, completed_with_risks, and
// hard_stop executor judgments survive supervisor handoff.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shinpr/galley/schemas"
)

func TestCodexCommandPlanMaterializesEmbeddedSchemaWhenOnlyContentAvailable(t *testing.T) {
	t.Parallel()
	attemptDir := t.TempDir()
	opts := CodexFromTask(minimalCodexTask())
	opts.WorkDir = "/tmp/codex-output-schema"
	opts.AttemptDir = attemptDir
	opts.Prompt = "work order"

	plan, err := CodexCommandPlan(opts)
	if err != nil {
		t.Fatalf("CodexCommandPlan: %v", err)
	}

	// Argv must include --output-schema pointing at a real file under the
	// attempt directory and --output-last-message under the same directory so
	// `codex exec` can dereference both paths.
	schemaFlag := flagValue(t, plan.Argv, "--output-schema")
	if schemaFlag == "" {
		t.Fatalf("argv missing --output-schema: %v", plan.Argv)
	}
	if filepath.Dir(schemaFlag) != attemptDir {
		t.Fatalf("output-schema not attempt-scoped: got %q, want under %q", schemaFlag, attemptDir)
	}
	body, err := os.ReadFile(schemaFlag)
	if err != nil {
		t.Fatalf("read materialized schema: %v", err)
	}
	if string(body) != schemas.ClaudeResult {
		t.Fatalf("materialized schema content drift: got %d bytes, want %d", len(body), len(schemas.ClaudeResult))
	}

	lastMsgFlag := flagValue(t, plan.Argv, "--output-last-message")
	if lastMsgFlag == "" {
		t.Fatalf("argv missing --output-last-message: %v", plan.Argv)
	}
	if filepath.Dir(lastMsgFlag) != attemptDir {
		t.Fatalf("output-last-message not attempt-scoped: got %q, want under %q", lastMsgFlag, attemptDir)
	}
	if filepath.Base(lastMsgFlag) != CodexLastMessageFilename {
		t.Fatalf("output-last-message filename drift: got %q, want %q", filepath.Base(lastMsgFlag), CodexLastMessageFilename)
	}
}

func TestCodexCommandPlanReusesCallerSuppliedSchemaFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	schemaPath := filepath.Join(dir, "external.schema.json")
	if err := os.WriteFile(schemaPath, []byte(`{"type":"object"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	attemptDir := t.TempDir()

	opts := CodexFromTask(minimalCodexTask())
	opts.WorkDir = "/tmp/codex-output-schema-file"
	opts.JSONSchemaFile = schemaPath
	opts.AttemptDir = attemptDir
	opts.Prompt = "work order"

	plan, err := CodexCommandPlan(opts)
	if err != nil {
		t.Fatalf("CodexCommandPlan: %v", err)
	}

	got := flagValue(t, plan.Argv, "--output-schema")
	if got != schemaPath {
		t.Fatalf("output-schema should reuse caller-supplied file: got %q, want %q", got, schemaPath)
	}
	// The runner must not write a second attempt-scoped schema file when the
	// caller already supplied a real path.
	if _, err := os.Stat(filepath.Join(attemptDir, CodexOutputSchemaFilename)); !os.IsNotExist(err) {
		t.Fatalf("attempt-scoped schema file should not be created when JSONSchemaFile is supplied: err=%v", err)
	}
}

func TestExtractCodexLastMessageFileParsesCompletedResult(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "codex.last-message.txt")
	body := `{"status":"completed","summary":"codex done","files_modified":["a"],"acceptance_criteria":[{"id":"AC1","status":"satisfied","evidence":["e"],"notes":"n"}],"verification":[],"decisions":[],"risks":[]}`
	if err := os.WriteFile(path, []byte(body+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := ExtractCodexLastMessageFile(path)
	if err != nil {
		t.Fatalf("ExtractCodexLastMessageFile: %v", err)
	}
	if got.Status != "completed" || got.Summary != "codex done" {
		t.Fatalf("parsed result drift: %#v", got)
	}
}

func TestExtractCodexLastMessageFileParsesHardStopResult(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "codex.last-message.txt")
	body := `{"status":"hard_stop","summary":"blocked","files_modified":[],"acceptance_criteria":[],"verification":[],"decisions":[],"risks":[],"hard_stop":{"reason":"missing dep","attempted":["installed dep"],"needed_to_continue":["network access"]}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := ExtractCodexLastMessageFile(path)
	if err != nil {
		t.Fatalf("ExtractCodexLastMessageFile: %v", err)
	}
	if got.Status != "hard_stop" || got.HardStop == nil {
		t.Fatalf("hard_stop result drift: %#v", got)
	}
	if got.HardStop.Reason != "missing dep" {
		t.Fatalf("hard_stop reason drift: %q", got.HardStop.Reason)
	}
}

// flagValue returns the argument that follows the named flag in argv, or the
// empty string when the flag is absent.
func flagValue(t *testing.T, argv []string, name string) string {
	t.Helper()
	for i := 0; i < len(argv)-1; i++ {
		if argv[i] == name {
			return strings.TrimSpace(argv[i+1])
		}
	}
	return ""
}
