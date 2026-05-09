package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractClaudeResultFromPlainJSON(t *testing.T) {
	t.Parallel()
	result, err := ExtractClaudeResult(`{"status":"completed","summary":"done","files_modified":[],"acceptance_criteria":[],"verification":[],"decisions":[],"risks":[]}`)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "completed" {
		t.Fatalf("status got %q", result.Status)
	}
}

func TestExtractClaudeResultFromStreamJSONResultString(t *testing.T) {
	t.Parallel()
	stdout := `{"type":"system","message":"start"}` + "\n" +
		`{"type":"result","result":"{\"status\":\"completed_with_risks\",\"summary\":\"done\",\"files_modified\":[],\"acceptance_criteria\":[],\"verification\":[],\"decisions\":[],\"risks\":[]}"}`
	result, err := ExtractClaudeResult(stdout)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "completed_with_risks" {
		t.Fatalf("status got %q", result.Status)
	}
}

func TestExtractClaudeResultFileReadsBeyondTailSizedNoise(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "claude.stdout.jsonl")
	noise := strings.Repeat("x", 70*1024)
	stdout := `{"type":"assistant","message":"` + noise + `"}` + "\n" +
		`{"type":"result","result":"{\"status\":\"completed\",\"summary\":\"done\",\"files_modified\":[\"file.txt\"],\"acceptance_criteria\":[{\"id\":\"AC1\",\"status\":\"satisfied\",\"evidence\":[\"checked\"],\"notes\":\"ok\"}],\"verification\":[{\"command\":\"test -f file.txt\",\"status\":\"passed\",\"reason\":\"file exists\",\"output_excerpt\":\"\"}],\"decisions\":[],\"risks\":[]}"}`
	if err := os.WriteFile(path, []byte(stdout), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := ExtractClaudeResultFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "completed" {
		t.Fatalf("status got %q", result.Status)
	}
}

func TestExtractClaudeResultRejectsMissingResult(t *testing.T) {
	t.Parallel()
	_, err := ExtractClaudeResult(`{"type":"system"}`)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestExtractClaudeResultRejectsMissingRequiredFields(t *testing.T) {
	t.Parallel()
	_, err := ExtractClaudeResult(`{"status":"completed"}`)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestExtractClaudeResultRejectsHardStopWithoutDetails(t *testing.T) {
	t.Parallel()
	_, err := ExtractClaudeResult(`{"status":"hard_stop","summary":"blocked","files_modified":[],"acceptance_criteria":[],"verification":[],"decisions":[],"risks":[]}`)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestExtractClaudeResultRejectsInvalidNestedEnums(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
	}{
		{name: "invalid acceptance status", input: `{"status":"completed","summary":"done","files_modified":[],"acceptance_criteria":[{"id":"AC1","status":"ok","evidence":[],"notes":""}],"verification":[],"decisions":[],"risks":[]}`},
		{name: "invalid verification status", input: `{"status":"completed","summary":"done","files_modified":[],"acceptance_criteria":[],"verification":[{"command":"test","status":"ok","reason":"","output_excerpt":""}],"decisions":[],"risks":[]}`},
		{name: "invalid decision reversibility", input: `{"status":"completed","summary":"done","files_modified":[],"acceptance_criteria":[],"verification":[],"decisions":[{"question":"q","chosen":"c","rationale":"r","reversibility":"sometimes","needs_human_review":false}],"risks":[]}`},
		{name: "invalid risk type", input: `{"status":"completed","summary":"done","files_modified":[],"acceptance_criteria":[],"verification":[],"decisions":[],"risks":[{"type":"bad","detail":"d","mitigation":"m","needs_human_review":false}]}`},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := ExtractClaudeResult(tt.input)
			if err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestExtractClaudeResultRejectsNestedRequiredFields(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
	}{
		{name: "missing acceptance id", input: `{"status":"completed","summary":"done","files_modified":[],"acceptance_criteria":[{"id":"","status":"satisfied","evidence":[],"notes":""}],"verification":[],"decisions":[],"risks":[]}`},
		{name: "missing verification command", input: `{"status":"completed","summary":"done","files_modified":[],"acceptance_criteria":[],"verification":[{"command":"","status":"passed","reason":"ok","output_excerpt":""}],"decisions":[],"risks":[]}`},
		{name: "missing decision question", input: `{"status":"completed","summary":"done","files_modified":[],"acceptance_criteria":[],"verification":[],"decisions":[{"question":"","chosen":"c","rationale":"r","reversibility":"high","needs_human_review":false}],"risks":[]}`},
		{name: "missing risk detail", input: `{"status":"completed","summary":"done","files_modified":[],"acceptance_criteria":[],"verification":[],"decisions":[],"risks":[{"type":"other","detail":"","mitigation":"m","needs_human_review":false}]}`},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := ExtractClaudeResult(tt.input)
			if err == nil {
				t.Fatal("expected error")
			}
		})
	}
}
