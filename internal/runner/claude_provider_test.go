package runner

import (
	"strconv"
	"strings"
	"testing"

	"github.com/shinpr/galley/internal/proc"
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

func TestConfigureClaudeProviderTrimsCredentialAndFailsFastWhenBlank(t *testing.T) {
	t.Parallel()
	tests := []struct {
		provider string
		raw      string
		wantEnv  string
	}{
		{provider: "glm", raw: ""},
		{provider: "glm", raw: "   "},
		{provider: "glm", raw: "  tok-123  ", wantEnv: "ANTHROPIC_AUTH_TOKEN=tok-123"},
		{provider: "kimi", raw: ""},
		{provider: "kimi", raw: "   "},
		{provider: "kimi", raw: "  tok-123  ", wantEnv: "ANTHROPIC_API_KEY=tok-123"},
	}
	for _, tt := range tests {
		t.Run(tt.provider+"/"+strconv.Quote(tt.raw), func(t *testing.T) {
			t.Parallel()
			opts := ClaudeProviderOptions{Provider: tt.provider, Credentials: ClaudeCredentials{GLMAuthToken: tt.raw}}
			if tt.provider == "kimi" {
				opts.Credentials = ClaudeCredentials{KimiAPIKey: tt.raw}
			}
			plan := proc.Command{Argv: []string{"claude", "-p"}}
			err := ConfigureClaudeProvider(&plan, opts)
			if validateErr := ValidateClaudeProvider(opts); (validateErr == nil) != (err == nil) {
				t.Fatalf("ValidateClaudeProvider = %v; ConfigureClaudeProvider = %v", validateErr, err)
			}
			if tt.wantEnv == "" {
				if err == nil || !strings.Contains(err.Error(), tt.provider+"_api_key") {
					t.Fatalf("error must name %s_api_key, got %v", tt.provider, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !containsEnvName(plan.EnvAppend, tt.wantEnv) {
				t.Fatalf("EnvAppend %#v missing %q", plan.EnvAppend, tt.wantEnv)
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
