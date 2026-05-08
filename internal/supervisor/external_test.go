package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestRunExternal(t *testing.T) {
	dir := t.TempDir()
	command := filepath.Join(dir, "supervisor")
	if err := os.WriteFile(command, []byte("#!/bin/sh\ncat >/dev/null\necho '{\"status\":\"accepted\",\"summary\":\"external ok\"}'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	verdict, err := RunExternal(context.Background(), ExternalOptions{Argv: []string{command}}, Evidence{})
	if err != nil {
		t.Fatal(err)
	}
	if verdict.Status != "accepted" {
		t.Fatalf("verdict got %#v", verdict)
	}
}

func TestValidateVerdictRejectsNeedsRevisionWithoutWorkOrder(t *testing.T) {
	err := ValidateVerdict(Verdict{Status: "needs_revision", Summary: "gaps"})
	if err == nil {
		t.Fatal("expected validation error")
	}
}

func TestSupervisorVerdictSchemaStatusEnumMatchesValidator(t *testing.T) {
	schemaPath := filepath.Join("..", "..", "schemas", "supervisor-verdict.schema.json")
	data, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Properties map[string]struct {
			Enum []string `json:"enum"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	got := append([]string(nil), schema.Properties["status"].Enum...)
	want := []string{"accepted", "hard_stop", "needs_revision", "needs_supervisor_review"}
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("schema status enum got %#v, want %#v", got, want)
	}
	for _, status := range got {
		if err := ValidateVerdict(Verdict{Status: status, Summary: "ok", NextWorkOrder: "work"}); err != nil && status != "needs_revision" {
			t.Fatalf("validator rejected schema status %q: %v", status, err)
		}
		if status == "needs_revision" {
			if err := ValidateVerdict(Verdict{Status: status, Summary: "ok", NextWorkOrder: "work"}); err != nil {
				t.Fatalf("validator rejected needs_revision with work order: %v", err)
			}
		}
	}
}

func TestNewExternalRequestSerializesErrorsAsStrings(t *testing.T) {
	request := NewExternalRequest(Evidence{
		ParseError:   errors.New("bad json"),
		RunError:     errors.New("executor failed"),
		DiffError:    errors.New("diff failed"),
		Diff:         "diff --git a/file b/file",
		DiffDirty:    true,
		Attempt:      2,
		AttemptsLeft: 1,
	})
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{
		`"parse_error":"bad json"`,
		`"run_error":"executor failed"`,
		`"diff_error":"diff failed"`,
		`"diff_dirty":true`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("request JSON missing %s: %s", want, text)
		}
	}
	if strings.Contains(text, `"ParseError"`) || strings.Contains(text, `"RunError"`) {
		t.Fatalf("request leaked Go field names: %s", text)
	}
}

func TestProviderSupervisorScriptsUseProviderPrompts(t *testing.T) {
	tests := []struct {
		name         string
		scriptPath   string
		promptPath   string
		wantContains []string
		wantNot      []string
	}{
		{
			name:       "codex",
			scriptPath: filepath.Join("..", "..", "scripts", "codex-supervisor.sh"),
			promptPath: filepath.Join("..", "..", "prompts", "codex-supervisor-review.md"),
			wantContains: []string{
				"prompts/codex-supervisor-review.md",
				"--output-schema",
				"supervisor-verdict.schema.json",
			},
		},
		{
			name:       "claude",
			scriptPath: filepath.Join("..", "..", "scripts", "claude-supervisor.sh"),
			promptPath: filepath.Join("..", "..", "prompts", "claude-supervisor-review.md"),
			wantContains: []string{
				"prompts/claude-supervisor-review.md",
				"--system-prompt",
				"--json-schema",
				"--output-format text",
				"supervisor-verdict.schema.json",
			},
			wantNot: []string{"--append-system-prompt"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			script, err := os.ReadFile(tt.scriptPath)
			if err != nil {
				t.Fatal(err)
			}
			prompt, err := os.ReadFile(tt.promptPath)
			if err != nil {
				t.Fatal(err)
			}
			if len(prompt) == 0 {
				t.Fatalf("%s is empty", tt.promptPath)
			}
			text := string(script)
			for _, want := range tt.wantContains {
				if !strings.Contains(text, want) {
					t.Fatalf("%s missing %q", tt.scriptPath, want)
				}
			}
			for _, banned := range tt.wantNot {
				if strings.Contains(text, banned) {
					t.Fatalf("%s contains banned token %q", tt.scriptPath, banned)
				}
			}
		})
	}
}

func TestClaudeSupervisorScriptEmitsVerdictFromClaude(t *testing.T) {
	dir := t.TempDir()
	fakeClaude := filepath.Join(dir, "claude")
	argsPath := filepath.Join(dir, "args.txt")
	stdinPath := filepath.Join(dir, "stdin.md")
	script := `#!/bin/sh
printf '%s\n' "$@" >"$GALLEY_FAKE_CLAUDE_ARGS"
cat >"$GALLEY_FAKE_CLAUDE_STDIN"
printf '{"status":"accepted","summary":"fake claude ok","acceptance_gaps":[],"quality_findings":[],"next_work_order":""}\n'
`
	if err := os.WriteFile(fakeClaude, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(filepath.Join("..", "..", "scripts", "claude-supervisor.sh"))
	cmd.Stdin = strings.NewReader(`{"evidence":{"attempt":1}}`)
	cmd.Env = append(os.Environ(),
		"PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"GALLEY_FAKE_CLAUDE_ARGS="+argsPath,
		"GALLEY_FAKE_CLAUDE_STDIN="+stdinPath,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("script failed: %v\n%s", err, out)
	}
	var verdict Verdict
	if err := json.Unmarshal(out, &verdict); err != nil {
		t.Fatalf("script did not emit verdict JSON: %v\n%s", err, out)
	}
	if verdict.Status != "accepted" {
		t.Fatalf("verdict got %#v", verdict)
	}
	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--system-prompt", "--json-schema", "--output-format", "text"} {
		if !strings.Contains(string(args), want) {
			t.Fatalf("claude args missing %q: %s", want, args)
		}
	}
	prompt, err := os.ReadFile(stdinPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"evidence"`, `"attempt":1`} {
		if !strings.Contains(string(prompt), want) {
			t.Fatalf("claude stdin missing %q: %s", want, prompt)
		}
	}
	if strings.Contains(string(prompt), "# Evidence JSON") {
		t.Fatalf("claude stdin should be pure JSON, got: %s", prompt)
	}
}
