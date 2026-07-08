package setup

// The setup executor is one of the executor roles, so executor.cli "glm" must
// redirect it to GLM's endpoint exactly like the implementation attempt, and
// fail fast when no token is configured.

import (
	"strings"
	"testing"

	"github.com/shinpr/galley/internal/task"
)

func glmSetupOptions(t *testing.T, token string) Options {
	t.Helper()
	return Options{
		Task: task.Task{
			ID:       "task-glm",
			Executor: task.Executor{CLI: "glm", Effort: "high"},
		},
		WorkDir:      t.TempDir(),
		RunDir:       t.TempDir(),
		GLMAuthToken: token,
	}
}

func TestBuildExecutorCommandPlanGLMRedirects(t *testing.T) {
	cmd, provider, err := BuildExecutorCommandPlan(glmSetupOptions(t, "zai-token"), []byte("{}"))
	if err != nil {
		t.Fatalf("BuildExecutorCommandPlan: %v", err)
	}
	if provider != "claude" {
		t.Fatalf("provider = %q, want claude (glm rides the Claude binary)", provider)
	}
	assertGLMEnv(t, cmd.EnvAppend, cmd.EnvRemove)
	if cmd.Argv[0] != "claude" {
		t.Fatalf("argv[0] = %q, want claude", cmd.Argv[0])
	}
}

func TestBuildExecutorCommandPlanGLMFailsFastWithoutToken(t *testing.T) {
	_, _, err := BuildExecutorCommandPlan(glmSetupOptions(t, ""), []byte("{}"))
	if err == nil {
		t.Fatal("expected fail-fast when glm setup executor has no token")
	}
	if !strings.Contains(err.Error(), "glm_api_key") {
		t.Fatalf("error must name the missing config key, got %q", err)
	}
}

// assertGLMEnv verifies the shared GLM redirect was applied to a command plan.
func assertGLMEnv(t *testing.T, envAppend, envRemove []string) {
	t.Helper()
	joined := strings.Join(envAppend, "\n")
	if !strings.Contains(joined, "ANTHROPIC_BASE_URL=https://api.z.ai/api/anthropic") {
		t.Fatalf("EnvAppend missing GLM base URL: %#v", envAppend)
	}
	if !strings.Contains(joined, "ANTHROPIC_AUTH_TOKEN=zai-token") {
		t.Fatalf("EnvAppend missing GLM auth token: %#v", envAppend)
	}
	found := false
	for _, k := range envRemove {
		if k == "ANTHROPIC_API_KEY" {
			found = true
		}
	}
	if !found {
		t.Fatalf("EnvRemove must strip ANTHROPIC_API_KEY: %#v", envRemove)
	}
}
