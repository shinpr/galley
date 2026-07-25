package runner

import (
	"fmt"
	"github.com/shinpr/galley/internal/proc"
	"strings"
)

const (
	GLMBaseURL  = "https://api.z.ai/api/anthropic"
	KimiBaseURL = "https://api.kimi.com/coding/"
)

type ClaudeCredentials struct {
	GLMAuthToken string
	KimiAPIKey   string
}

type ClaudeProviderOptions struct {
	Provider    string
	Credentials ClaudeCredentials
}

// ConfigureClaudeProvider applies endpoint and credential differences after the shared Claude command is built.
func ConfigureClaudeProvider(plan *proc.Command, opts ClaudeProviderOptions) error {
	baseURL, authEnv, credential, err := claudeProviderEnvironment(opts)
	if err != nil || baseURL == "" {
		return err
	}
	plan.EnvRemove = append(plan.EnvRemove, "ANTHROPIC_BASE_URL", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN")
	plan.EnvAppend = append(plan.EnvAppend, "ANTHROPIC_BASE_URL="+baseURL, authEnv+"="+credential)
	return nil
}

func ValidateClaudeProvider(opts ClaudeProviderOptions) error {
	_, _, _, err := claudeProviderEnvironment(opts)
	return err
}

func claudeProviderEnvironment(opts ClaudeProviderOptions) (string, string, string, error) {
	switch opts.Provider {
	case "", "claude":
		return "", "", "", nil
	case "glm":
		return requiredClaudeCredential("glm", GLMBaseURL, "ANTHROPIC_AUTH_TOKEN", opts.Credentials.GLMAuthToken)
	case "kimi":
		return requiredClaudeCredential("kimi", KimiBaseURL, "ANTHROPIC_API_KEY", opts.Credentials.KimiAPIKey)
	default:
		return "", "", "", fmt.Errorf("provider %q does not use the Claude transport", opts.Provider)
	}
}

func requiredClaudeCredential(provider, baseURL, authEnv, raw string) (string, string, string, error) {
	credential := strings.TrimSpace(raw)
	if credential == "" {
		return "", "", "", fmt.Errorf("cli is %q but no API key is configured; set %s_api_key in daemon.yaml", provider, provider)
	}
	return baseURL, authEnv, credential, nil
}
