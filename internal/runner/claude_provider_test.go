package runner

import (
	"github.com/shinpr/galley/internal/proc"
	"strings"
	"testing"
)

func TestConfigureClaudeProviderKimiUsesDedicatedEndpointAndAPIKey(t *testing.T) {
	plan := proc.Command{Argv: []string{"claude", "-p"}}
	err := ConfigureClaudeProvider(&plan, ClaudeProviderOptions{
		Provider:    "kimi",
		Credentials: ClaudeCredentials{KimiAPIKey: "kimi-token"},
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(plan.EnvAppend, "\n")
	if !strings.Contains(joined, "ANTHROPIC_BASE_URL=https://api.kimi.com/coding/") {
		t.Fatalf("EnvAppend missing Kimi base URL: %#v", plan.EnvAppend)
	}
	if !strings.Contains(joined, "ANTHROPIC_API_KEY=kimi-token") {
		t.Fatalf("EnvAppend missing Kimi API key: %#v", plan.EnvAppend)
	}
	if !containsEnvName(plan.EnvRemove, "ANTHROPIC_AUTH_TOKEN") {
		t.Fatalf("EnvRemove must strip ANTHROPIC_AUTH_TOKEN: %#v", plan.EnvRemove)
	}
	if strings.Contains(strings.Join(plan.Argv, " "), "kimi-token") {
		t.Fatalf("argv leaked Kimi API key: %#v", plan.Argv)
	}
}

func TestConfigureClaudeProviderFailsFastWithoutConfiguredCredential(t *testing.T) {
	for _, provider := range []string{"glm", "kimi"} {
		provider := provider
		t.Run(provider, func(t *testing.T) {
			plan := proc.Command{Argv: []string{"claude", "-p"}}
			err := ConfigureClaudeProvider(&plan, ClaudeProviderOptions{Provider: provider})
			if err == nil {
				t.Fatal("expected missing credential error")
			}
			if !strings.Contains(err.Error(), provider+"_api_key") {
				t.Fatalf("error must name %s_api_key, got %q", provider, err)
			}
		})
	}
}

func containsEnvName(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
