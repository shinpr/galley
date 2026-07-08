package runner

import (
	"fmt"
	"strings"
)

// GLMBaseURL is GLM's Anthropic-compatible endpoint (Z.ai).
//
// "glm" is not a separate binary or code path: it means "run the Claude Code
// binary against this endpoint". That single fact is why glm is a valid cli
// value everywhere a Claude executor or supervisor is valid (implementation,
// setup, acceptance-skeleton, and review), and why there is deliberately no
// glm-specific launch logic beyond the env redirect below — a future reader
// should not expect one, and should not add a glm branch that some Claude call
// sites have and others lack.
const GLMBaseURL = "https://api.z.ai/api/anthropic"

// RedirectClaudeToGLM points an already-built Claude command plan at GLM's
// endpoint by injecting the base URL and auth token into the child environment
// and stripping any inherited ANTHROPIC_API_KEY. Stripping matters for both
// correctness and security: a stale real Anthropic key would otherwise take
// precedence over the GLM token (failing auth against Z.ai) and be sent to a
// third party. The token travels only through EnvAppend, which is never
// serialized into run evidence and never placed on argv.
func RedirectClaudeToGLM(plan *Command, token string) {
	plan.EnvAppend = append(plan.EnvAppend,
		"ANTHROPIC_BASE_URL="+GLMBaseURL,
		"ANTHROPIC_AUTH_TOKEN="+token,
	)
	plan.EnvRemove = append(plan.EnvRemove, "ANTHROPIC_API_KEY")
}

// ResolveGLMToken trims and validates the configured GLM token. It returns an
// actionable, secret-free error when a glm cli was selected without a token so
// every glm call site fails fast with the same clear config message instead of
// an opaque downstream 401.
func ResolveGLMToken(raw string) (string, error) {
	token := strings.TrimSpace(raw)
	if token == "" {
		return "", fmt.Errorf("cli is \"glm\" but no GLM auth token is configured; set glm_api_key in daemon.yaml")
	}
	return token, nil
}
