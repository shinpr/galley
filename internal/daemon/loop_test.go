package daemon

import (
	"testing"
)

func TestExecutorVerificationCmdUnknownIsStable(t *testing.T) {
	t.Parallel()
	if got := executorVerificationCmd("opus-cli"); got != "unknown" {
		t.Fatalf("executorVerificationCmd unknown got %q, want unknown", got)
	}
}

func TestExecutorVerificationCmdEmptyUsesClaudeDefault(t *testing.T) {
	t.Parallel()
	if got := executorVerificationCmd(""); got != "claude -p" {
		t.Fatalf("executorVerificationCmd empty got %q, want claude -p", got)
	}
}
