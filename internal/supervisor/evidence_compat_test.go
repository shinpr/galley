package supervisor

import (
	"encoding/json"
	"testing"
)

func TestAdapterEvidencePreservesClaudeCompatibilityKey(t *testing.T) {
	t.Parallel()
	data, err := json.Marshal(AdapterEvidence{})
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if _, ok := decoded["claude"]; !ok {
		t.Fatalf("adapter evidence lost compatibility key: %s", data)
	}
}
