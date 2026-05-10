package supervisor

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/shinpr/galley/internal/task"
)

// TestEvidencePreflightResultAndCheckpointsPropagateToAdapterRequest proves the
// daemon-supplied acceptance skeleton preflight result and the latest attempt's
// skeleton checkpoint evidence are carried through Evidence -> AdapterRequest and
// serialized under their stable JSON keys so a model supervisor can read them.
func TestEvidencePreflightResultAndCheckpointsPropagateToAdapterRequest(t *testing.T) {
	t.Parallel()
	request := NewAdapterRequest(Evidence{
		Attempt:      2,
		AttemptsLeft: 1,
		Task:         task.Task{Scope: task.Scope{CWD: "/source/repo"}},
		PreflightResult: map[string]any{
			"status": "completed",
			"outputs": []map[string]any{{
				"ac_id":                   "AC1",
				"path":                    "internal/foo/foo_test.go",
				"implementation_required": true,
				"checkpoint_command":      "go test ./internal/foo/",
			}},
		},
		SkeletonCheckpointResults: []PreflightCheckpointResult{{
			ACID:    "AC1",
			Command: "go test ./internal/foo/",
			Status:  "passed",
			Source:  "acceptance_skeleton",
		}},
	})

	if request.Evidence.PreflightResult == nil {
		t.Fatal("preflight result not propagated to adapter request")
	}
	if len(request.Evidence.SkeletonCheckpointResults) != 1 || request.Evidence.SkeletonCheckpointResults[0].ACID != "AC1" {
		t.Fatalf("skeleton checkpoint results not propagated: %#v", request.Evidence.SkeletonCheckpointResults)
	}

	data, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{
		`"preflight_result":`,
		`"skeleton_checkpoint_results":`,
		`"ac_id":"AC1"`,
		`"command":"go test ./internal/foo/"`,
		`"status":"passed"`,
		`"source":"acceptance_skeleton"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("adapter request JSON missing %s: %s", want, text)
		}
	}
}

// TestEvidencePreflightFieldsOmittedWhenAbsent proves tasks without the
// acceptance skeleton preflight stage do not emit empty preflight keys, so the
// supervisor evidence shape is unchanged for the common case.
func TestEvidencePreflightFieldsOmittedWhenAbsent(t *testing.T) {
	t.Parallel()
	request := NewAdapterRequest(Evidence{Attempt: 1, AttemptsLeft: 2})
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if strings.Contains(text, "preflight_result") {
		t.Fatalf("expected preflight_result omitted when absent: %s", text)
	}
	if strings.Contains(text, "skeleton_checkpoint_results") {
		t.Fatalf("expected skeleton_checkpoint_results omitted when absent: %s", text)
	}
}
