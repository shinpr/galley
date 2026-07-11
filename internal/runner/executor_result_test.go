package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractExecutorResultFromPlainJSON(t *testing.T) {
	t.Parallel()
	result, err := ExtractExecutorResult(`{"status":"completed","summary":"done","files_modified":[],"acceptance_criteria":[],"verification":[],"scope_expansions":[],"decisions":[],"risks":[]}`)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "completed" {
		t.Fatalf("status got %q", result.Status)
	}
}

func TestExtractExecutorResultAcceptsScopeExpansions(t *testing.T) {
	t.Parallel()
	result, err := ExtractExecutorResult(`{"status":"completed","summary":"done","files_modified":["internal/task/workorder.go"],"acceptance_criteria":[],"verification":[],"scope_expansions":[{"path":"internal/task","reason":"revision required task work order changes","linked_requirement":"revision:pr-comment-1","minimality":"only the work order renderer changed"}],"decisions":[],"risks":[]}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.ScopeExpansions) != 1 || result.ScopeExpansions[0].LinkedRequirement != "revision:pr-comment-1" {
		t.Fatalf("scope expansions got %#v", result.ScopeExpansions)
	}
}

func TestExtractExecutorResultFromStreamJSONResultString(t *testing.T) {
	t.Parallel()
	stdout := `{"type":"system","message":"start"}` + "\n" +
		`{"type":"result","result":"{\"status\":\"completed_with_risks\",\"summary\":\"done\",\"files_modified\":[],\"acceptance_criteria\":[],\"verification\":[],\"scope_expansions\":[],\"decisions\":[],\"risks\":[]}"}`
	result, err := ExtractExecutorResult(stdout)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "completed_with_risks" {
		t.Fatalf("status got %q", result.Status)
	}
}

func TestExtractExecutorResultFileReadsBeyondTailSizedNoise(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "claude.stdout.jsonl")
	noise := strings.Repeat("x", 70*1024)
	stdout := `{"type":"assistant","message":"` + noise + `"}` + "\n" +
		`{"type":"result","result":"{\"status\":\"completed\",\"summary\":\"done\",\"files_modified\":[\"file.txt\"],\"acceptance_criteria\":[{\"id\":\"AC1\",\"status\":\"satisfied\",\"evidence\":[\"checked\"],\"notes\":\"ok\"}],\"verification\":[{\"command\":\"test -f file.txt\",\"status\":\"passed\",\"reason\":\"file exists\",\"output_excerpt\":\"\"}],\"scope_expansions\":[],\"decisions\":[],\"risks\":[]}"}`
	if err := os.WriteFile(path, []byte(stdout), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := ExtractExecutorResultFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "completed" {
		t.Fatalf("status got %q", result.Status)
	}
}

func TestExtractExecutorResultRejectsMissingResult(t *testing.T) {
	t.Parallel()
	_, err := ExtractExecutorResult(`{"type":"system"}`)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestExtractExecutorResultRejectsMissingRequiredFields(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
	}{
		{name: "missing most fields", input: `{"status":"completed"}`},
		{name: "missing scope expansions", input: `{"status":"completed","summary":"done","files_modified":[],"acceptance_criteria":[],"verification":[],"decisions":[],"risks":[]}`},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := ExtractExecutorResult(tt.input)
			if err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestExtractExecutorResultRejectsHardStopWithoutDetails(t *testing.T) {
	t.Parallel()
	_, err := ExtractExecutorResult(`{"status":"hard_stop","summary":"blocked","files_modified":[],"acceptance_criteria":[],"verification":[],"decisions":[],"risks":[]}`)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestExtractExecutorResultRejectsInvalidNestedEnums(t *testing.T) {
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
			_, err := ExtractExecutorResult(tt.input)
			if err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestExtractExecutorResultRejectsNestedRequiredFields(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
	}{
		{name: "missing acceptance id", input: `{"status":"completed","summary":"done","files_modified":[],"acceptance_criteria":[{"id":"","status":"satisfied","evidence":[],"notes":""}],"verification":[],"decisions":[],"risks":[]}`},
		{name: "missing verification command", input: `{"status":"completed","summary":"done","files_modified":[],"acceptance_criteria":[],"verification":[{"command":"","status":"passed","reason":"ok","output_excerpt":""}],"decisions":[],"risks":[]}`},
		{name: "missing scope expansion path", input: `{"status":"completed","summary":"done","files_modified":[],"acceptance_criteria":[],"verification":[],"scope_expansions":[{"path":"","reason":"needed","linked_requirement":"AC1","minimality":"small"}],"decisions":[],"risks":[]}`},
		{name: "absolute scope expansion path", input: `{"status":"completed","summary":"done","files_modified":[],"acceptance_criteria":[],"verification":[],"scope_expansions":[{"path":"/tmp/foo.go","reason":"needed","linked_requirement":"AC1","minimality":"small"}],"decisions":[],"risks":[]}`},
		{name: "windows drive scope expansion path", input: `{"status":"completed","summary":"done","files_modified":[],"acceptance_criteria":[],"verification":[],"scope_expansions":[{"path":"C:/tmp/foo.go","reason":"needed","linked_requirement":"AC1","minimality":"small"}],"decisions":[],"risks":[]}`},
		{name: "backslash scope expansion path", input: `{"status":"completed","summary":"done","files_modified":[],"acceptance_criteria":[],"verification":[],"scope_expansions":[{"path":"internal\\foo.go","reason":"needed","linked_requirement":"AC1","minimality":"small"}],"decisions":[],"risks":[]}`},
		{name: "double slash scope expansion path", input: `{"status":"completed","summary":"done","files_modified":[],"acceptance_criteria":[],"verification":[],"scope_expansions":[{"path":"internal//foo.go","reason":"needed","linked_requirement":"AC1","minimality":"small"}],"decisions":[],"risks":[]}`},
		{name: "trailing slash scope expansion path", input: `{"status":"completed","summary":"done","files_modified":[],"acceptance_criteria":[],"verification":[],"scope_expansions":[{"path":"internal/foo/","reason":"needed","linked_requirement":"AC1","minimality":"small"}],"decisions":[],"risks":[]}`},
		{name: "unclean scope expansion path", input: `{"status":"completed","summary":"done","files_modified":[],"acceptance_criteria":[],"verification":[],"scope_expansions":[{"path":"internal/../foo.go","reason":"needed","linked_requirement":"AC1","minimality":"small"}],"decisions":[],"risks":[]}`},
		{name: "parent traversal scope expansion path", input: `{"status":"completed","summary":"done","files_modified":[],"acceptance_criteria":[],"verification":[],"scope_expansions":[{"path":"../foo.go","reason":"needed","linked_requirement":"AC1","minimality":"small"}],"decisions":[],"risks":[]}`},
		{name: "missing scope expansion reason", input: `{"status":"completed","summary":"done","files_modified":[],"acceptance_criteria":[],"verification":[],"scope_expansions":[{"path":"internal/foo.go","reason":"","linked_requirement":"AC1","minimality":"small"}],"decisions":[],"risks":[]}`},
		{name: "missing scope expansion linked requirement", input: `{"status":"completed","summary":"done","files_modified":[],"acceptance_criteria":[],"verification":[],"scope_expansions":[{"path":"internal/foo.go","reason":"needed","linked_requirement":"","minimality":"small"}],"decisions":[],"risks":[]}`},
		{name: "missing scope expansion minimality", input: `{"status":"completed","summary":"done","files_modified":[],"acceptance_criteria":[],"verification":[],"scope_expansions":[{"path":"internal/foo.go","reason":"needed","linked_requirement":"AC1","minimality":""}],"decisions":[],"risks":[]}`},
		{name: "missing decision question", input: `{"status":"completed","summary":"done","files_modified":[],"acceptance_criteria":[],"verification":[],"decisions":[{"question":"","chosen":"c","rationale":"r","reversibility":"high","needs_human_review":false}],"risks":[]}`},
		{name: "missing risk detail", input: `{"status":"completed","summary":"done","files_modified":[],"acceptance_criteria":[],"verification":[],"decisions":[],"risks":[{"type":"other","detail":"","mitigation":"m","needs_human_review":false}]}`},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := ExtractExecutorResult(tt.input)
			if err == nil {
				t.Fatal("expected error")
			}
		})
	}
}
