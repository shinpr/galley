package runner

// GLM redirect: "glm" is the Claude binary pointed at GLM's endpoint. The
// shared redirect injects the endpoint + token into the child environment and
// strips any inherited ANTHROPIC_API_KEY, and the token never reaches argv.

import (
	"strings"
	"testing"
)

func TestRedirectClaudeToGLMInjectsEndpointAndTokenViaEnvOnly(t *testing.T) {
	t.Parallel()
	const token = "zai-secret-token"
	plan := Command{Argv: []string{"claude", "-p", "--model", "glm-4.6"}}

	RedirectClaudeToGLM(&plan, token)

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

func TestResolveGLMTokenTrimsAndValidates(t *testing.T) {
	t.Parallel()
	if got, err := ResolveGLMToken("  tok-123  "); err != nil || got != "tok-123" {
		t.Fatalf("ResolveGLMToken trim = (%q, %v), want (%q, nil)", got, err, "tok-123")
	}
	for _, raw := range []string{"", "   "} {
		if _, err := ResolveGLMToken(raw); err == nil {
			t.Fatalf("ResolveGLMToken(%q) expected error", raw)
		} else if !strings.Contains(err.Error(), "glm_api_key") {
			t.Fatalf("error must name the config key, got %q", err)
		}
	}
}
