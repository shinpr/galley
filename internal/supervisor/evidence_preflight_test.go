package supervisor

import (
	"encoding/json"
	"strings"
	"testing"
)

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
