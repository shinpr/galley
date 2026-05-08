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

	"github.com/shinpr/galley/internal/profile"
	"github.com/shinpr/galley/internal/task"
)

func TestRunExternal(t *testing.T) {
	dir := t.TempDir()
	command := filepath.Join(dir, "supervisor")
	if err := os.WriteFile(command, []byte("#!/bin/sh\ncat >/dev/null\necho '{\"status\":\"accepted\",\"summary\":\"external ok\",\"acceptance_gaps\":[],\"quality_findings\":[],\"reviewed_files\":[],\"acceptance_evidence\":[],\"findings\":[],\"residual_risks\":[],\"confidence\":\"high\",\"next_work_order\":\"\"}'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	artifactDir := filepath.Join(dir, "artifacts")
	workDir := filepath.Join(dir, "worktree")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	verdict, err := RunExternal(context.Background(), ExternalOptions{Argv: []string{command}, WorkDir: workDir, ArtifactDir: artifactDir}, Evidence{})
	if err != nil {
		t.Fatal(err)
	}
	if verdict.Status != "accepted" {
		t.Fatalf("verdict got %#v", verdict)
	}
	for _, name := range []string{"supervisor_request.json", "supervisor_stdout.log", "supervisor_stderr.log"} {
		if _, err := os.Stat(filepath.Join(artifactDir, name)); err != nil {
			t.Fatalf("artifact %s missing: %v", name, err)
		}
	}
	requestData, err := os.ReadFile(filepath.Join(artifactDir, "supervisor_request.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(requestData), `"worktree_cwd":"`+workDir+`"`) {
		t.Fatalf("request JSON missing worktree cwd: %s", requestData)
	}
}

func TestValidateVerdictRejectsNeedsRevisionWithoutWorkOrder(t *testing.T) {
	err := ValidateVerdict(Verdict{Status: "needs_revision", Summary: "gaps", Confidence: "high"})
	if err == nil {
		t.Fatal("expected validation error")
	}
}

func TestValidateVerdictRequiresConfidence(t *testing.T) {
	err := ValidateVerdict(Verdict{Status: "accepted", Summary: "ok"})
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "confidence") {
		t.Fatalf("unexpected error: %v", err)
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
		if err := ValidateVerdict(Verdict{Status: status, Summary: "ok", Confidence: "high", NextWorkOrder: "work"}); err != nil && status != "needs_revision" {
			t.Fatalf("validator rejected schema status %q: %v", status, err)
		}
		if status == "needs_revision" {
			if err := ValidateVerdict(Verdict{Status: status, Summary: "ok", Confidence: "high", NextWorkOrder: "work"}); err != nil {
				t.Fatalf("validator rejected needs_revision with work order: %v", err)
			}
		}
	}
}

func TestValidateVerdictForEvidenceRejectsAcceptedWithoutRepositoryReview(t *testing.T) {
	err := ValidateVerdictForEvidence(Verdict{
		Status:             "accepted",
		Summary:            "ok",
		AcceptanceEvidence: []AcceptanceEvidence{{ACID: "AC1", Evidence: []string{"diff"}}},
		Confidence:         "high",
	}, Evidence{
		Task:      task.Task{AcceptanceCriteria: []task.AcceptanceCriterion{{ID: "AC1", Text: "done"}}},
		DiffDirty: true,
	})
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "reviewed_files") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateVerdictForEvidenceRejectsAcceptedWithoutACEvidence(t *testing.T) {
	err := ValidateVerdictForEvidence(Verdict{
		Status:        "accepted",
		Summary:       "ok",
		ReviewedFiles: []string{"file.go"},
		Confidence:    "high",
	}, Evidence{
		Task:      task.Task{AcceptanceCriteria: []task.AcceptanceCriterion{{ID: "AC1", Text: "done"}}},
		DiffDirty: true,
	})
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "AC1") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateVerdictForEvidenceRejectsAcceptedWithBlockingFinding(t *testing.T) {
	err := ValidateVerdictForEvidence(Verdict{
		Status:             "accepted",
		Summary:            "ok",
		ReviewedFiles:      []string{"file.go"},
		AcceptanceEvidence: []AcceptanceEvidence{{ACID: "AC1", Evidence: []string{"diff"}}},
		Findings:           []Finding{{Severity: "medium", Category: "correctness", Summary: "ordering bug", BlocksAcceptance: false}},
		Confidence:         "high",
	}, Evidence{
		Task:      task.Task{AcceptanceCriteria: []task.AcceptanceCriterion{{ID: "AC1", Text: "done"}}},
		DiffDirty: true,
	})
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "blocks_acceptance") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateVerdictForEvidenceDefaultAllowsLowSeverityFinding(t *testing.T) {
	err := ValidateVerdictForEvidence(Verdict{
		Status:             "accepted",
		Summary:            "ok",
		ReviewedFiles:      []string{"file.go"},
		AcceptanceEvidence: []AcceptanceEvidence{{ACID: "AC1", Evidence: []string{"diff"}}},
		Findings:           []Finding{{Severity: "low", Category: "style", Summary: "wording", BlocksAcceptance: false}},
		Confidence:         "medium",
	}, Evidence{
		Task:      task.Task{AcceptanceCriteria: []task.AcceptanceCriterion{{ID: "AC1", Text: "done"}}},
		DiffDirty: true,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestValidateVerdictForEvidenceBlockingSeveritiesCanRequireLowSeverityFinding(t *testing.T) {
	err := ValidateVerdictForEvidence(Verdict{
		Status:             "accepted",
		Summary:            "ok",
		ReviewedFiles:      []string{"file.go"},
		AcceptanceEvidence: []AcceptanceEvidence{{ACID: "AC1", Evidence: []string{"diff"}}},
		Findings:           []Finding{{Severity: "low", Category: "style", Summary: "wording", BlocksAcceptance: false}},
		Confidence:         "medium",
	}, Evidence{
		Task: task.Task{AcceptanceCriteria: []task.AcceptanceCriterion{{ID: "AC1", Text: "done"}}},
		Profiles: profile.Bundle{Quality: &profile.Quality{PassPolicy: profile.PassPolicy{
			BlockingSeverities: []string{"critical", "high", "medium", "low"},
		}}},
		DiffDirty: true,
	})
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "blocks_acceptance") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateVerdictForEvidenceRejectsBlocksAcceptanceMismatch(t *testing.T) {
	err := ValidateVerdictForEvidence(Verdict{
		Status:             "accepted",
		Summary:            "ok",
		ReviewedFiles:      []string{"file.go"},
		AcceptanceEvidence: []AcceptanceEvidence{{ACID: "AC1", Evidence: []string{"diff"}}},
		Findings:           []Finding{{Severity: "low", Category: "style", Summary: "wording", BlocksAcceptance: true}},
		Confidence:         "medium",
	}, Evidence{
		Task:      task.Task{AcceptanceCriteria: []task.AcceptanceCriterion{{ID: "AC1", Text: "done"}}},
		DiffDirty: true,
	})
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "blocks_acceptance") {
		t.Fatalf("unexpected error: %v", err)
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
		Task: task.Task{Scope: task.Scope{
			CWD: "/source/repo",
		}},
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
		`"source_cwd":"/source/repo"`,
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
				"--json",
				"codex_supervisor_events.jsonl",
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
				"--tools default",
				"--allowedTools",
				"claude_supervisor_debug.log",
				"supervisor-verdict.schema.json",
			},
			wantNot: []string{"--append-system-prompt", `--tools ""`},
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
printf '{"status":"accepted","summary":"fake claude ok","acceptance_gaps":[],"quality_findings":[],"reviewed_files":[],"acceptance_evidence":[],"findings":[],"residual_risks":[],"confidence":"high","next_work_order":""}\n'
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
	for _, want := range []string{"--system-prompt", "--json-schema", "--output-format", "text", "--tools", "default", "--allowedTools"} {
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
