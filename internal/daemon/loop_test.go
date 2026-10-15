package daemon

import (
	"testing"
)

func TestExecutorVerificationCmdNamesExecutorTransport(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		cli  string
		want string
	}{
		{cli: "", want: "claude -p"},
		{cli: "claude", want: "claude -p"},
		{cli: "codex", want: "codex exec"},
		{cli: "grok", want: "grok"},
		{cli: "glm", want: "claude -p (glm)"},
		{cli: "kimi", want: "claude -p (kimi)"},
		{cli: "opus-cli", want: "unknown"},
	} {
		t.Run(tc.cli, func(t *testing.T) {
			t.Parallel()
			if got := executorVerificationCmd(tc.cli); got != tc.want {
				t.Fatalf("executorVerificationCmd(%q) = %q, want %q", tc.cli, got, tc.want)
			}
		})
	}
}
