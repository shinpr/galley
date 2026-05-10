package supervisor

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/shinpr/galley/internal/task"
)

// TestEvidencePreflightResultPropagatesToAdapterRequest proves the
// daemon-supplied acceptance skeleton preflight result is carried through
// Evidence -> AdapterRequest and serialized under its stable JSON key so a
// model supervisor can read it.
func TestEvidencePreflightResultPropagatesToAdapterRequest(t *testing.T) {
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
			}},
		},
	})

	if request.Evidence.PreflightResult == nil {
		t.Fatal("preflight result not propagated to adapter request")
	}

	data, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{
		`"preflight_result":`,
		`"ac_id":"AC1"`,
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
}
