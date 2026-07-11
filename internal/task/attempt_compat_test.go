package task

import (
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestAttemptPreservesClaudeStatusCompatibilityKey(t *testing.T) {
	t.Parallel()
	data, err := yaml.Marshal(Attempt{Number: 1, ClaudeStatus: "completed"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "claude_status: completed") {
		t.Fatalf("attempt YAML lost compatibility key:\n%s", data)
	}
}
