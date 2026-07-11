package runner

import (
	"encoding/json"
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
	if strings.Contains(string(body), `"allOf"`) {
		t.Fatalf("Codex output schema must not contain allOf; schema=%s", string(body))
	}
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("materialized schema is not valid JSON: %v", err)
	}
	if got := doc["title"]; got != "Galley Claude Executor Result" {
		t.Fatalf("materialized schema title drift: %#v", got)
	}
	props, ok := doc["properties"].(map[string]any)
	if !ok {
		t.Fatalf("materialized schema missing properties object: %#v", doc["properties"])
	}
	required, ok := doc["required"].([]any)
	if !ok {
		t.Fatalf("materialized schema missing required array: %#v", doc["required"])
	}
	requiredSet := make(map[string]bool, len(required))
	for _, value := range required {
		name, ok := value.(string)
		if !ok {
			t.Fatalf("required entry is not a string: %#v", value)
		}
		requiredSet[name] = true
	}
	for name := range props {
		if !requiredSet[name] {
			t.Fatalf("Codex output schema must require property %q", name)
		}
	}
	hardStop, ok := props["hard_stop"].(map[string]any)
	if !ok {
		t.Fatalf("materialized schema missing hard_stop object: %#v", props["hard_stop"])
	}
	types, ok := hardStop["type"].([]any)
	if !ok || len(types) != 2 || types[0] != "object" || types[1] != "null" {
		t.Fatalf("hard_stop must be nullable for Codex strict schema: %#v", hardStop["type"])
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

func TestCodexCompatibleOutputSchemaRecursivelyRequiresObjectProperties(t *testing.T) {
	t.Parallel()
	body, err := CodexCompatibleOutputSchema(schemas.SetupResult)
	if err != nil {
		t.Fatalf("CodexCompatibleOutputSchema: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatalf("compatible setup schema is not valid JSON: %v", err)
	}

	props := objectProp(t, doc, "properties")
	topRequired := requiredSet(t, doc)
	for name := range props {
		if !topRequired[name] {
			t.Fatalf("top-level setup schema property %q is not required", name)
		}
	}
	successful := objectProp(t, props, "successful_commands")
	if !typeAllowsNull(successful["type"]) {
		t.Fatalf("optional successful_commands must be nullable for Codex strict schema: %#v", successful["type"])
	}
	readiness := objectProp(t, props, "readiness_evidence")
	if !typeAllowsNull(readiness["type"]) {
		t.Fatalf("optional readiness_evidence must be nullable for Codex strict schema: %#v", readiness["type"])
	}

	commands := objectProp(t, props, "commands")
	commandItems := objectProp(t, commands, "items")
	commandProps := objectProp(t, commandItems, "properties")
	commandRequired := requiredSet(t, commandItems)
	for name := range commandProps {
		if !commandRequired[name] {
			t.Fatalf("commands.items property %q is not required", name)
		}
	}
	why := objectProp(t, commandProps, "why")
	if !typeAllowsNull(why["type"]) {
		t.Fatalf("optional commands.items.why must be nullable: %#v", why["type"])
	}
	stdout := objectProp(t, commandProps, "stdout_excerpt")
	if !typeAllowsNull(stdout["type"]) {
		t.Fatalf("optional commands.items.stdout_excerpt must be nullable: %#v", stdout["type"])
	}

	successItems := objectProp(t, successful, "items")
	successRequired := requiredSet(t, successItems)
	if !successRequired["why"] {
		t.Fatalf("successful_commands.items.why must be required for Codex strict schema")
	}
	successWhy := objectProp(t, objectProp(t, successItems, "properties"), "why")
	if !typeAllowsNull(successWhy["type"]) {
		t.Fatalf("optional successful_commands.items.why must be nullable: %#v", successWhy["type"])
	}
}

func TestCodexExecutorResultSchemaRemovesUnsupportedPatterns(t *testing.T) {
	t.Parallel()
	body, err := CodexExecutorResultSchema()
	if err != nil {
		t.Fatalf("CodexExecutorResultSchema: %v", err)
	}
	var doc any
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatalf("compatible executor schema is not valid JSON: %v", err)
	}
	assertNoSchemaKeyword(t, doc, "pattern")
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
	body := `{"status":"completed","summary":"codex done","files_modified":["a"],"acceptance_criteria":[{"id":"AC1","status":"satisfied","evidence":["e"],"notes":"n"}],"verification":[],"scope_expansions":[],"decisions":[],"risks":[]}`
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
	body := `{"status":"hard_stop","summary":"blocked","files_modified":[],"acceptance_criteria":[],"verification":[],"scope_expansions":[],"decisions":[],"risks":[],"hard_stop":{"reason":"missing dep","attempted":["installed dep"],"needed_to_continue":["network access"]}}`
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

func objectProp(t *testing.T, parent map[string]any, name string) map[string]any {
	t.Helper()
	got, ok := parent[name].(map[string]any)
	if !ok {
		t.Fatalf("property %q is not an object: %#v", name, parent[name])
	}
	return got
}

func assertNoSchemaKeyword(t *testing.T, node any, keyword string) {
	t.Helper()
	switch value := node.(type) {
	case map[string]any:
		if _, ok := value[keyword]; ok {
			t.Fatalf("Codex output schema contains unsupported %q keyword: %#v", keyword, value)
		}
		for _, child := range value {
			assertNoSchemaKeyword(t, child, keyword)
		}
	case []any:
		for _, child := range value {
			assertNoSchemaKeyword(t, child, keyword)
		}
	}
}

func requiredSet(t *testing.T, schema map[string]any) map[string]bool {
	t.Helper()
	raw, ok := schema["required"].([]any)
	if !ok {
		t.Fatalf("schema missing required array: %#v", schema["required"])
	}
	out := make(map[string]bool, len(raw))
	for _, item := range raw {
		name, ok := item.(string)
		if !ok {
			t.Fatalf("required entry is not a string: %#v", item)
		}
		out[name] = true
	}
	return out
}

func typeAllowsNull(raw any) bool {
	switch value := raw.(type) {
	case string:
		return value == "null"
	case []any:
		for _, item := range value {
			if item == "null" {
				return true
			}
		}
	}
	return false
}
