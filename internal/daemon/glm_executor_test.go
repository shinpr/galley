package daemon

import (
	"strings"
	"testing"
)

func TestClaudeProviderCredentialFailsFast(t *testing.T) {
	for _, provider := range []string{"glm", "kimi"} {
		provider := provider
		t.Run(provider, func(t *testing.T) {
			err := validateProviderCredential(provider, Options{})
			if err == nil {
				t.Fatal("expected fail-fast error when provider credential is missing")
			}
			if !strings.Contains(err.Error(), provider+"_api_key") {
				t.Fatalf("error must name the missing config key, got %q", err)
			}
		})
	}
}

func TestExecutorVerificationCmdRedirectedClaudeProviders(t *testing.T) {
	for _, provider := range []string{"glm", "kimi"} {
		want := "claude -p (" + provider + ")"
		if got := executorVerificationCmd(provider); got != want {
			t.Fatalf("executorVerificationCmd(%s) = %q, want %q", provider, got, want)
		}
	}
}
