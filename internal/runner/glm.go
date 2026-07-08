package runner

import (
	"fmt"
	"strings"
)

// GLMBaseURL is GLM's Anthropic-compatible endpoint (Z.ai). "glm" is not a
// separate binary: it is the Claude Code binary pointed here, which is why it is
// valid anywhere a Claude executor or supervisor is, with no glm-specific launch
// logic beyond the redirect below.
const GLMBaseURL = "https://api.z.ai/api/anthropic"

// RedirectClaudeToGLM points a built Claude command plan at GLM's endpoint via
// the child env and strips any inherited ANTHROPIC_API_KEY so a stale Anthropic
// key cannot override the GLM token or leak to Z.ai. The token stays in
// EnvAppend (json:"-"), never on argv or in evidence.
func RedirectClaudeToGLM(plan *Command, token string) {
	plan.EnvAppend = append(plan.EnvAppend,
		"ANTHROPIC_BASE_URL="+GLMBaseURL,
		"ANTHROPIC_AUTH_TOKEN="+token,
	)
	plan.EnvRemove = append(plan.EnvRemove, "ANTHROPIC_API_KEY")
}

// ResolveGLMToken trims and validates the token, returning a secret-free config
// error so every glm call site fails fast instead of hitting a downstream 401.
func ResolveGLMToken(raw string) (string, error) {
	token := strings.TrimSpace(raw)
	if token == "" {
		return "", fmt.Errorf("cli is \"glm\" but no GLM auth token is configured; set glm_api_key in daemon.yaml")
	}
	return token, nil
}
