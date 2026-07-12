package runner

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shinpr/galley/internal/task"
	"github.com/shinpr/galley/prompts"
)

func TestGrokCommandPlanUsesPromptFileAndSchema(t *testing.T) {
	dir := t.TempDir()
	attemptDir := filepath.Join(dir, "space 日本語")
	plan, err := GrokCommandPlan(GrokOptions{Bin: "grok-test", WorkDir: dir, AttemptDir: attemptDir, Prompt: "secret work order", SystemPrompt: "role", JSONSchema: `{"type":"object"}`, PermissionMode: "bypassPermissions", Sandbox: "workspace", Effort: "high"})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(plan.Argv, " ")
	if strings.Contains(joined, "secret work order") || plan.Stdin != "" {
		t.Fatalf("prompt leaked outside file: %#v", plan)
	}
	if !strings.Contains(joined, "--prompt-file") || !strings.Contains(joined, "--json-schema") || !strings.Contains(joined, "--permission-mode bypassPermissions") || !strings.Contains(joined, "--sandbox workspace") || !strings.Contains(joined, "--reasoning-effort high") {
		t.Fatalf("incomplete grok argv: %#v", plan.Argv)
	}
	body, err := os.ReadFile(filepath.Join(attemptDir, GrokPromptFilename))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "role") || !strings.Contains(string(body), "secret work order") {
		t.Fatalf("prompt file = %q", body)
	}
}

func TestGrokFromTaskPermissionMapping(t *testing.T) {
	tests := []struct{ permission, sandbox string }{{"", "workspace"}, {"edit", "workspace"}, {"read-only", "read-only"}, {"sandbox-full-access", "off"}}
	for _, tc := range tests {
		t.Run(tc.permission, func(t *testing.T) {
			got := GrokFromTask(task.Task{Scope: task.Scope{Permission: tc.permission}})
			if got.PermissionMode != "bypassPermissions" || got.Sandbox != tc.sandbox {
				t.Fatalf("permission %q mapped to mode=%q sandbox=%q; want bypassPermissions/%q", tc.permission, got.PermissionMode, got.Sandbox, tc.sandbox)
			}
		})
	}
}

func TestGrokPromptsContainNoCodexRuntimeContract(t *testing.T) {
	for name, prompt := range map[string]string{"executor": prompts.GrokExecutorFull(), "setup": prompts.SetupExecutorGrok(), "skeleton": prompts.AcceptanceSkeletonCreatorGrok()} {
		for _, forbidden := range []string{"running in Codex", "Codex shell", "--output-last-message", "Codex sandbox"} {
			if strings.Contains(prompt, forbidden) {
				t.Fatalf("%s prompt contains %q", name, forbidden)
			}
		}
		if !strings.Contains(prompt, "Grok Build") {
			t.Fatalf("%s prompt is not Grok-specific", name)
		}
	}
}

func TestExtractGrokExecutorResultRequiresEndTurnAndValidJSON(t *testing.T) {
	valid := `{"status":"completed","summary":"done","files_modified":[],"acceptance_criteria":[],"verification":[],"scope_expansions":[],"decisions":[],"risks":[]}`
	text, _ := json.Marshal(valid)
	if _, err := ExtractGrokExecutorResult([]byte(`{"text":` + string(text) + `,"stopReason":"EndTurn","sessionId":"s"}`)); err != nil {
		t.Fatalf("valid envelope: %v", err)
	}
	if _, err := ExtractGrokExecutorResult([]byte(`{"text":` + string(text) + `,"stopReason":"Cancelled","sessionId":"s"}`)); err == nil {
		t.Fatal("partial completion accepted")
	}
	if _, err := ExtractGrokExecutorResult([]byte(`{"text":"not json","stopReason":"EndTurn"}`)); err == nil {
		t.Fatal("invalid result accepted")
	}
}

func TestExtractGrokExecutorResultPrefersStructuredOutput(t *testing.T) {
	valid := `{"status":"completed","summary":"done","files_modified":[],"acceptance_criteria":[],"verification":[],"scope_expansions":[],"decisions":[],"risks":[]}`
	envelope := `{"text":"{}{}","structuredOutput":` + valid + `,"stopReason":"EndTurn","sessionId":"s"}`
	result, err := ExtractGrokExecutorResult([]byte(envelope))
	if err != nil {
		t.Fatal(err)
	}
	if result.Summary != "done" {
		t.Fatalf("result = %#v", result)
	}
}

func TestDecodeGrokEnvelopeNegativeMatrixAndMetadata(t *testing.T) {
	tests := []struct {
		name, input    string
		wantCompletion bool
	}{
		{"cancelled", `{"text":"{}","stopReason":"Cancelled","sessionId":"cancel-id"}`, true},
		{"turn limit", `{"text":"{}","stopReason":"MaxTurns","sessionId":"turn-id"}`, true},
		{"missing text", `{"stopReason":"EndTurn","sessionId":"empty-id"}`, false},
		{"missing stop reason", `{"text":"{}","sessionId":"missing-stop"}`, false},
		{"missing session id", `{"text":"{}","stopReason":"EndTurn"}`, false},
		{"malformed", `{`, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := DecodeGrokEnvelope([]byte(tc.input))
			if err == nil {
				t.Fatal("invalid envelope accepted")
			}
			var completion *GrokCompletionError
			if errors.As(err, &completion) != tc.wantCompletion {
				t.Fatalf("completion error=%T; wantCompletion=%t", err, tc.wantCompletion)
			}
		})
	}
	path := filepath.Join(t.TempDir(), "completion.json")
	if err := WriteGrokCompletionMetadata(path, []byte(`{"text":"{}","stopReason":"Cancelled","sessionId":"session-1"}`)); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"stop_reason": "Cancelled"`) || !strings.Contains(string(body), `"session_id": "session-1"`) {
		t.Fatalf("metadata = %s", body)
	}
	if err := WriteGrokCompletionMetadata(path, []byte(`{"text":"{}","sessionId":"missing-stop"}`)); err != nil {
		t.Fatal(err)
	}
	body, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"classification": "malformed_envelope"`) || !strings.Contains(string(body), "stopReason is required") {
		t.Fatalf("missing-field metadata = %s", body)
	}
}
