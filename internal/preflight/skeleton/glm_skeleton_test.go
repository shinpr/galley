package skeleton

// The acceptance-skeleton creator is one of the executor roles, so executor.cli
// "glm" must redirect it to GLM's endpoint exactly like the implementation
// attempt, and fail fast when no token is configured.

import (
	"strings"
	"testing"

	"github.com/shinpr/galley/internal/task"
)

func glmSkeletonOptions(t *testing.T, token string) Options {
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

func TestBuildBuiltinCreatorCommandPlanGLMRedirects(t *testing.T) {
	cmd, perr := buildBuiltinCreatorCommandPlan(glmSkeletonOptions(t, "zai-token"), []byte("{}"))
	if perr != nil {
		t.Fatalf("buildBuiltinCreatorCommandPlan: %v", perr)
	}
	joined := strings.Join(cmd.EnvAppend, "\n")
	if !strings.Contains(joined, "ANTHROPIC_BASE_URL=https://api.z.ai/api/anthropic") {
		t.Fatalf("EnvAppend missing GLM base URL: %#v", cmd.EnvAppend)
	}
	if !strings.Contains(joined, "ANTHROPIC_AUTH_TOKEN=zai-token") {
		t.Fatalf("EnvAppend missing GLM auth token: %#v", cmd.EnvAppend)
	}
	found := false
	for _, k := range cmd.EnvRemove {
		if k == "ANTHROPIC_API_KEY" {
			found = true
		}
	}
	if !found {
		t.Fatalf("EnvRemove must strip ANTHROPIC_API_KEY: %#v", cmd.EnvRemove)
	}
}

func TestBuildBuiltinCreatorCommandPlanGLMFailsFastWithoutToken(t *testing.T) {
	_, perr := buildBuiltinCreatorCommandPlan(glmSkeletonOptions(t, ""), []byte("{}"))
	if perr == nil {
		t.Fatal("expected fail-fast when glm skeleton creator has no token")
	}
	if !strings.Contains(perr.Error(), "glm_api_key") {
		t.Fatalf("error must name the missing config key, got %q", perr)
	}
}
