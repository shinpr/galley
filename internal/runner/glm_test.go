package runner

import (
	"strings"
	"testing"
)

func TestConfigureClaudeProviderGLMInjectsEndpointAndTokenViaEnvOnly(t *testing.T) {
	t.Parallel()
	const token = "zai-secret-token"
	plan := Command{Argv: []string{"claude", "-p", "--model", "glm-4.6"}}

	if err := ConfigureClaudeProvider(&plan, ClaudeProviderOptions{
		Provider:    "glm",
		Credentials: ClaudeCredentials{GLMAuthToken: token},
	}); err != nil {
		t.Fatal(err)
	}

	want := map[string]bool{
		"ANTHROPIC_BASE_URL=https://api.z.ai/api/anthropic": false,
		"ANTHROPIC_AUTH_TOKEN=" + token:                     false,
	}
	for _, kv := range plan.EnvAppend {
		if _, ok := want[kv]; ok {
			want[kv] = true
		}
	}
	for kv, seen := range want {
		if !seen {
			t.Fatalf("EnvAppend %#v missing %q", plan.EnvAppend, kv)
		}
	}
	found := false
	for _, k := range plan.EnvRemove {
		if k == "ANTHROPIC_API_KEY" {
			found = true
		}
	}
	if !found {
		t.Fatalf("EnvRemove %#v must strip ANTHROPIC_API_KEY", plan.EnvRemove)
	}
	// The token must never reach argv, where it would appear in process
	// listings and be recorded into command-plan evidence.
	for _, arg := range plan.Argv {
		if strings.Contains(arg, token) {
			t.Fatalf("token leaked onto argv: %#v", plan.Argv)
		}
	}
}

func TestValidateClaudeProviderGLMTrimsAndValidates(t *testing.T) {
	t.Parallel()
	if err := ValidateClaudeProvider(ClaudeProviderOptions{
		Provider:    "glm",
		Credentials: ClaudeCredentials{GLMAuthToken: "  tok-123  "},
	}); err != nil {
		t.Fatalf("ValidateClaudeProvider rejected a trimmed token: %v", err)
	}
	for _, raw := range []string{"", "   "} {
		err := ValidateClaudeProvider(ClaudeProviderOptions{
			Provider:    "glm",
			Credentials: ClaudeCredentials{GLMAuthToken: raw},
		})
		if err == nil {
			t.Fatalf("ValidateClaudeProvider(%q) expected error", raw)
		} else if !strings.Contains(err.Error(), "glm_api_key") {
			t.Fatalf("error must name the config key, got %q", err)
		}
	}
}
