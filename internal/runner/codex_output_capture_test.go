package runner

import (
	"bytes"
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
	if strings.Contains(string(body), `"pattern"`) {
		t.Fatalf("Codex output schema must not contain pattern; schema=%s", string(body))
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

// AC1: normalization must descend through every supported schema-bearing branch
// (anyOf, oneOf, $defs, additionalProperties) so nested gaps cannot survive.
func TestCodexCompatibleOutputSchemaNormalizesNestedSchemaBranches(t *testing.T) {
	t.Parallel()
	schema := `{
  "type": "object",
  "$defs": {"Role": {"type": "string", "pattern": "^[a-z]+$", "enum": ["admin"]}},
  "properties": {
    "primary": {
      "anyOf": [
        {"type": "object", "allOf": [{"required": ["code"]}], "properties": {"code": {"type": "string", "pattern": "^[0-9]+$"}}, "required": []},
        {"type": "null"}
      ]
    },
    "extras": {"type": "object", "additionalProperties": {"type": "string", "pattern": "@"}, "properties": {"note": {"type": "string", "uniqueItems": true}}, "required": []},
    "secondary": {"oneOf": [{"type": "object", "properties": {"kind": {"type": "string", "pattern": "^k-"}}, "required": []}]}
  },
  "required": ["primary", "extras", "secondary"]
}`
	body, err := CodexCompatibleOutputSchema(schema)
	if err != nil {
		t.Fatalf("CodexCompatibleOutputSchema: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatalf("normalized nested schema invalid JSON: %v", err)
	}
	for _, bad := range []string{"allOf", "pattern", "uniqueItems"} {
		assertNoSchemaKeyword(t, doc, bad)
	}
	props := objectProp(t, doc, "properties")

	anyBranches := objectProp(t, props, "primary")["anyOf"].([]any)
	anyBranch := anyBranches[0].(map[string]any)
	if !requiredSet(t, anyBranch)["code"] {
		t.Fatalf("anyOf branch optional 'code' must be required: %#v", anyBranch["required"])
	}
	code := objectProp(t, objectProp(t, anyBranch, "properties"), "code")
	if !typeAllowsNull(code["type"]) {
		t.Fatalf("anyOf branch optional 'code' must be null-allowed: %#v", code["type"])
	}

	extras := objectProp(t, props, "extras")
	addl, ok := extras["additionalProperties"].(map[string]any)
	if !ok {
		t.Fatalf("extras.additionalProperties must remain a schema: %#v", extras["additionalProperties"])
	}
	if _, still := addl["pattern"]; still {
		t.Fatalf("additionalProperties schema must strip pattern: %#v", addl)
	}

	defs, ok := doc["$defs"].(map[string]any)
	if !ok {
		t.Fatalf("$defs must remain a map after normalization: %#v", doc["$defs"])
	}
	if _, still := objectProp(t, defs, "Role")["pattern"]; still {
		t.Fatalf("$defs Role must strip pattern: %#v", defs["Role"])
	}

	oneBranches := objectProp(t, props, "secondary")["oneOf"].([]any)
	oneBranch := oneBranches[0].(map[string]any)
	if !requiredSet(t, oneBranch)["kind"] {
		t.Fatalf("oneOf branch optional 'kind' must be required: %#v", oneBranch["required"])
	}
	kind := objectProp(t, objectProp(t, oneBranch, "properties"), "kind")
	if !typeAllowsNull(kind["type"]) {
		t.Fatalf("oneOf branch optional 'kind' must be null-allowed: %#v", kind["type"])
	}
}

func TestCodexCommandPlanConvergesCanonicalSchemaInputs(t *testing.T) {
	t.Parallel()
	canonical := codexCanonicalTestSchema
	cases := []struct {
		name   string
		inline string
		file   string
	}{
		{name: "inline-json-schema", inline: canonical},
		{name: "canonical-schema-file", file: writeCanonicalSchemaFile(t, canonical)},
	}
	derivatives := make([][]byte, 0, len(cases))
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			attemptDir := t.TempDir()
			opts := CodexFromTask(minimalCodexTask())
			opts.WorkDir = "/tmp/codex-schema-boundary"
			opts.AttemptDir = attemptDir
			opts.JSONSchema = tc.inline
			opts.JSONSchemaFile = tc.file
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
				t.Fatalf("output-schema must be attempt-scoped derivative: got %q, want under %q", schemaFlag, attemptDir)
			}
			if tc.file != "" && schemaFlag == tc.file {
				t.Fatalf("argv must reference normalized derivative, not canonical file %q", tc.file)
			}
			derivative, err := os.ReadFile(schemaFlag)
			if err != nil {
				t.Fatalf("read materialized derivative: %v", err)
			}
			assertCodexNormalized(t, derivative)
			if tc.file != "" {
				source, err := os.ReadFile(tc.file)
				if err != nil {
					t.Fatalf("read canonical source: %v", err)
				}
				if !bytes.Equal(source, []byte(canonical)) {
					t.Fatalf("canonical source file must not be mutated")
				}
			}
			derivatives = append(derivatives, derivative)
		})
	}
	if !bytes.Equal(derivatives[0], derivatives[1]) {
		t.Fatalf("canonical inputs must converge on one normalized artifact")
	}
}

func TestCodexCommandPlanFailsBeforeExecOnInvalidSchema(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		setup func(t *testing.T, opts *CodexOptions)
		want  string
	}{
		{name: "attempt-dir-malformed-inline", setup: func(t *testing.T, o *CodexOptions) {
			o.JSONSchema = `{not json`
			o.AttemptDir = t.TempDir()
		}, want: "decode codex output schema"},
		{name: "caller-destination-malformed-inline", setup: func(t *testing.T, o *CodexOptions) {
			o.JSONSchema = `{not json`
			o.OutputSchemaFile = filepath.Join(t.TempDir(), "caller.schema.json")
		}, want: "decode codex output schema"},
		{name: "caller-destination-missing-file", setup: func(t *testing.T, o *CodexOptions) {
			o.JSONSchemaFile = filepath.Join(t.TempDir(), "missing.schema.json")
			o.OutputSchemaFile = filepath.Join(t.TempDir(), "caller.schema.json")
		}, want: "read codex output schema file"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			opts := CodexFromTask(minimalCodexTask())
			opts.Prompt = "work order"
			tc.setup(t, &opts)
			_, err := CodexCommandPlan(opts)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected error containing %q, got %v", tc.want, err)
			}
		})
	}
}

// AC1/AC3: a preselected OutputSchemaFile is a destination only, so the
// boundary still decodes and normalizes the canonical schema into it.
func TestCodexCommandPlanMaterializesSchemaAtCallerSelectedDestination(t *testing.T) {
	t.Parallel()
	dest := filepath.Join(t.TempDir(), "caller.schema.json")
	opts := CodexFromTask(minimalCodexTask())
	opts.WorkDir = "/tmp/codex-output-schema-dest"
	opts.JSONSchema = codexCanonicalTestSchema
	opts.OutputSchemaFile = dest
	opts.Prompt = "work order"

	plan, err := CodexCommandPlan(opts)
	if err != nil {
		t.Fatalf("CodexCommandPlan: %v", err)
	}
	if schemaFlag := flagValue(t, plan.Argv, "--output-schema"); schemaFlag != dest {
		t.Fatalf("argv --output-schema must reference caller destination: got %q want %q", schemaFlag, dest)
	}
	body, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read materialized derivative at caller destination: %v", err)
	}
	assertCodexNormalized(t, body)
}

// AC3: explicit canonical schema input without a destination must fail before
// execution rather than silently omitting --output-schema.
func TestCodexCommandPlanErrorsOnCanonicalSchemaWithoutDestination(t *testing.T) {
	t.Parallel()
	opts := CodexFromTask(minimalCodexTask())
	opts.WorkDir = "/tmp/codex-output-schema-dest"
	opts.JSONSchema = codexCanonicalTestSchema
	opts.Prompt = "work order"
	_, err := CodexCommandPlan(opts)
	if err == nil || !strings.Contains(err.Error(), "codex output schema destination is required") {
		t.Fatalf("expected missing-destination error, got %v", err)
	}
}

func TestPrepareCodexOutputSchemaReportsSchemaPreparationFailures(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cases := []struct {
		name string
		opts CodexOptions
		want string
	}{
		{name: "malformed-inline-json", opts: CodexOptions{JSONSchema: `{not json`}, want: "decode codex output schema"},
		{name: "malformed-file-json", opts: CodexOptions{JSONSchemaFile: writeCanonicalSchemaFile(t, `{not json`)}, want: "decode codex output schema"},
		{name: "missing-schema-file", opts: CodexOptions{JSONSchemaFile: filepath.Join(dir, "missing.schema.json")}, want: "read codex output schema file"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := PrepareCodexOutputSchema(tc.opts, t.TempDir(), CodexOutputSchemaFilename)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q must identify %q", err.Error(), tc.want)
			}
		})
	}
}

// AC1: when the destination aliases the canonical source file (exact-path, symlink,
// or hard link), the boundary must reject it before writing so canonical bytes survive.
func TestPrepareCodexOutputSchemaRejectsAliasedDestination(t *testing.T) {
	t.Parallel()
	canonical := codexCanonicalTestSchema
	sourcePath := writeCanonicalSchemaFile(t, canonical)
	want := "aliases canonical source"

	cases := []struct {
		name  string
		setup func(t *testing.T) (destDir, filename string)
	}{
		{name: "exact-path", setup: func(t *testing.T) (string, string) {
			return filepath.Dir(sourcePath), filepath.Base(sourcePath)
		}},
		{name: "cleaned-path", setup: func(t *testing.T) (string, string) {
			return filepath.Dir(sourcePath), "./" + filepath.Base(sourcePath)
		}},
		{name: "symlink-alias", setup: func(t *testing.T) (string, string) {
			linkDir := t.TempDir()
			link := filepath.Join(linkDir, "link.schema.json")
			if err := os.Symlink(sourcePath, link); err != nil {
				t.Skipf("symlink unsupported: %v", err)
			}
			return linkDir, "link.schema.json"
		}},
		{name: "hard-link-alias", setup: func(t *testing.T) (string, string) {
			linkDir := t.TempDir()
			link := filepath.Join(linkDir, "hard.schema.json")
			if err := os.Link(sourcePath, link); err != nil {
				t.Skipf("hard link unsupported: %v", err)
			}
			return linkDir, "hard.schema.json"
		}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			destDir, filename := tc.setup(t)
			before, err := os.ReadFile(sourcePath)
			if err != nil {
				t.Fatalf("read canonical source before: %v", err)
			}
			_, err = PrepareCodexOutputSchema(CodexOptions{JSONSchemaFile: sourcePath}, destDir, filename)
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("expected error containing %q, got %v", want, err)
			}
			after, err := os.ReadFile(sourcePath)
			if err != nil {
				t.Fatalf("read canonical source after: %v", err)
			}
			if !bytes.Equal(before, after) {
				t.Fatalf("canonical source bytes must remain unchanged: before=%q after=%q", string(before), string(after))
			}
			if bytes.Equal(after, []byte(canonical)) == false {
				t.Fatalf("canonical source must still equal original canonical bytes")
			}
		})
	}
}

// AC1: a distinct caller destination must still succeed and normalize, proving
// the alias rejection does not over-block legitimate distinct destinations.
func TestPrepareCodexOutputSchemaAcceptsDistinctDestination(t *testing.T) {
	t.Parallel()
	sourcePath := writeCanonicalSchemaFile(t, codexCanonicalTestSchema)
	destDir := t.TempDir()
	path, err := PrepareCodexOutputSchema(CodexOptions{JSONSchemaFile: sourcePath}, destDir, CodexOutputSchemaFilename)
	if err != nil {
		t.Fatalf("PrepareCodexOutputSchema distinct destination: %v", err)
	}
	if filepath.Dir(path) != destDir {
		t.Fatalf("distinct derivative must live in destDir: got %q", path)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read derivative: %v", err)
	}
	assertCodexNormalized(t, body)
	source, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatalf("read canonical source: %v", err)
	}
	if !bytes.Equal(source, []byte(codexCanonicalTestSchema)) {
		t.Fatalf("canonical source must remain unchanged for distinct destination")
	}
}

// AC1 boundary: the alias rejection must surface through CodexCommandPlan so the
// argv never references an aliased canonical source as --output-schema.
func TestCodexCommandPlanRejectsAliasedOutputSchemaFile(t *testing.T) {
	t.Parallel()
	sourcePath := writeCanonicalSchemaFile(t, codexCanonicalTestSchema)
	opts := CodexFromTask(minimalCodexTask())
	opts.WorkDir = "/tmp/codex-alias-plan"
	opts.JSONSchemaFile = sourcePath
	opts.OutputSchemaFile = sourcePath
	opts.Prompt = "work order"
	_, err := CodexCommandPlan(opts)
	if err == nil || !strings.Contains(err.Error(), "aliases canonical source") {
		t.Fatalf("expected alias error through CodexCommandPlan, got %v", err)
	}
	source, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatalf("read canonical source: %v", err)
	}
	if !bytes.Equal(source, []byte(codexCanonicalTestSchema)) {
		t.Fatalf("canonical source must remain unchanged when plan rejects alias")
	}
}

// AC1: Draft 2020-12 contentSchema is a schema-bearing location; incompatible
// keywords beneath it must be stripped and optional properties required/null-allowed.
func TestCodexCompatibleOutputSchemaNormalizesContentSchema(t *testing.T) {
	t.Parallel()
	schema := `{
  "type": "object",
  "properties": {
    "payload": {
      "type": "string",
      "contentMediaType": "application/json",
      "contentSchema": {
        "type": "object",
        "pattern": "^.*$",
        "uniqueItems": true,
        "allOf": [{"required": ["code"]}],
        "properties": {
          "code": {"type": "string", "pattern": "^[0-9]+$"},
          "note": {"type": "string", "enum": ["a", "b"]}
        },
        "required": ["code"]
      }
    }
  },
  "required": ["payload"]
}`
	body, err := CodexCompatibleOutputSchema(schema)
	if err != nil {
		t.Fatalf("CodexCompatibleOutputSchema: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatalf("normalized contentSchema doc invalid JSON: %v", err)
	}
	for _, bad := range []string{"allOf", "pattern", "uniqueItems"} {
		assertNoSchemaKeyword(t, doc, bad)
	}
	payload := objectProp(t, objectProp(t, doc, "properties"), "payload")
	contentSchema := objectProp(t, payload, "contentSchema")
	if _, still := contentSchema["allOf"]; still {
		t.Fatalf("contentSchema must strip allOf: %#v", contentSchema)
	}
	if _, still := contentSchema["pattern"]; still {
		t.Fatalf("contentSchema must strip pattern: %#v", contentSchema)
	}
	if _, still := contentSchema["uniqueItems"]; still {
		t.Fatalf("contentSchema must strip uniqueItems: %#v", contentSchema)
	}
	contentProps := objectProp(t, contentSchema, "properties")
	contentRequired := requiredSet(t, contentSchema)
	if !contentRequired["note"] {
		t.Fatalf("contentSchema optional 'note' must be required: %#v", contentSchema["required"])
	}
	note := objectProp(t, contentProps, "note")
	if !typeAllowsNull(note["type"]) {
		t.Fatalf("contentSchema optional 'note' must be null-allowed: %#v", note["type"])
	}
	if !enumAllowsNull(note["enum"]) {
		t.Fatalf("contentSchema optional 'note' enum must allow null: %#v", note["enum"])
	}
	code := objectProp(t, contentProps, "code")
	if _, still := code["pattern"]; still {
		t.Fatalf("contentSchema nested 'code' must strip pattern: %#v", code)
	}
}

// codexCanonicalTestSchema carries every Codex-incompatible keyword the
// boundary must normalize away: allOf, pattern, uniqueItems, optional enums.
const codexCanonicalTestSchema = `{
  "title": "Galley Codex Schema Boundary Test",
  "type": "object",
  "allOf": [{"required": ["name"]}],
  "properties": {
    "name": {"type": "string", "pattern": "^[a-z]+$"},
    "tags": {"type": "array", "items": {"type": "string"}, "uniqueItems": true},
    "profile": {
      "type": "object",
      "properties": {
        "email": {"type": "string", "pattern": "@"},
        "role": {"type": "string", "enum": ["admin", "user"]}
      },
      "required": ["email"]
    },
    "status": {"type": "string", "enum": ["active", "archived"]}
  },
  "required": ["name", "tags", "profile"]
}`

func writeCanonicalSchemaFile(t *testing.T, schema string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "canonical.schema.json")
	if err := os.WriteFile(path, []byte(schema), 0o600); err != nil {
		t.Fatalf("write canonical schema: %v", err)
	}
	return path
}

func assertCodexNormalized(t *testing.T, body []byte) {
	t.Helper()
	for _, bad := range []string{`"allOf"`, `"pattern"`, `"uniqueItems"`} {
		if strings.Contains(string(body), bad) {
			t.Fatalf("derivative must not contain %s: %s", bad, string(body))
		}
	}
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("derivative is not valid JSON: %v", err)
	}
	props := objectProp(t, doc, "properties")
	status := objectProp(t, props, "status")
	if !typeAllowsNull(status["type"]) {
		t.Fatalf("optional status must be nullable: %#v", status["type"])
	}
	if !enumAllowsNull(status["enum"]) {
		t.Fatalf("optional status enum must allow null: %#v", status["enum"])
	}
	role := objectProp(t, objectProp(t, objectProp(t, props, "profile"), "properties"), "role")
	if !typeAllowsNull(role["type"]) {
		t.Fatalf("optional nested role must be nullable: %#v", role["type"])
	}
	if !enumAllowsNull(role["enum"]) {
		t.Fatalf("optional nested role enum must allow null: %#v", role["enum"])
	}
}

func enumAllowsNull(raw any) bool {
	items, ok := raw.([]any)
	if !ok {
		return false
	}
	for _, item := range items {
		if item == nil {
			return true
		}
	}
	return false
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
