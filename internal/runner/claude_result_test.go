package runner

import "testing"

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
	tests := []string{
		`{"status":"completed","summary":"done","files_modified":[],"acceptance_criteria":[{"id":"AC1","status":"ok","evidence":[],"notes":""}],"verification":[],"decisions":[],"risks":[]}`,
		`{"status":"completed","summary":"done","files_modified":[],"acceptance_criteria":[],"verification":[{"command":"test","status":"ok","reason":"","output_excerpt":""}],"decisions":[],"risks":[]}`,
		`{"status":"completed","summary":"done","files_modified":[],"acceptance_criteria":[],"verification":[],"decisions":[{"question":"q","chosen":"c","rationale":"r","reversibility":"sometimes","needs_human_review":false}],"risks":[]}`,
		`{"status":"completed","summary":"done","files_modified":[],"acceptance_criteria":[],"verification":[],"decisions":[],"risks":[{"type":"bad","detail":"d","mitigation":"m","needs_human_review":false}]}`,
	}
	for _, input := range tests {
		input := input
		t.Run(input[:30], func(t *testing.T) {
			t.Parallel()
			_, err := ExtractClaudeResult(input)
			if err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestExtractClaudeResultRejectsNestedRequiredFields(t *testing.T) {
	t.Parallel()
	tests := []string{
		`{"status":"completed","summary":"done","files_modified":[],"acceptance_criteria":[{"id":"","status":"satisfied","evidence":[],"notes":""}],"verification":[],"decisions":[],"risks":[]}`,
		`{"status":"completed","summary":"done","files_modified":[],"acceptance_criteria":[],"verification":[{"command":"","status":"passed","reason":"ok","output_excerpt":""}],"decisions":[],"risks":[]}`,
		`{"status":"completed","summary":"done","files_modified":[],"acceptance_criteria":[],"verification":[],"decisions":[{"question":"","chosen":"c","rationale":"r","reversibility":"high","needs_human_review":false}],"risks":[]}`,
		`{"status":"completed","summary":"done","files_modified":[],"acceptance_criteria":[],"verification":[],"decisions":[],"risks":[{"type":"other","detail":"","mitigation":"m","needs_human_review":false}]}`,
	}
	for _, input := range tests {
		input := input
		t.Run(input[:30], func(t *testing.T) {
			t.Parallel()
			_, err := ExtractClaudeResult(input)
			if err == nil {
				t.Fatal("expected error")
			}
		})
	}
}
