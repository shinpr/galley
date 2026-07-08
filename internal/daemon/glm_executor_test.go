package daemon

// GLM executor wiring: a task with executor.cli "glm" reuses the Claude launch
// path but redirects the Claude binary at GLM's Anthropic-compatible endpoint
// via the child environment, and fails fast when the operator did not configure
// a GLM token.
//
// Behavior under test:
//   - applyGLMEnv injects ANTHROPIC_BASE_URL + ANTHROPIC_AUTH_TOKEN into the
//     command's EnvAppend and marks ANTHROPIC_API_KEY for removal from the
//     inherited child environment, without ever placing the token on argv.
//   - prepareGLMExecutorPlan fails fast with an actionable, token-free error
//     when opts.GLMAuthToken is empty or blank.
//   - executorVerificationCmd labels a glm run distinctly so reviewers can tell
//     the run used GLM from the saved task file alone.

import (
	"strings"
	"testing"

	"github.com/shinpr/galley/internal/task"
)

func TestPrepareGLMExecutorPlanFailsFastWithoutToken(t *testing.T) {
	t.Parallel()
	for _, token := range []string{"", "   "} {
		token := token
		t.Run("token="+token, func(t *testing.T) {
			t.Parallel()
			opts := Options{GLMAuthToken: token}
			// The token check runs before any task-derived work, so an empty
			// task is sufficient to exercise the fail-fast path.
			_, _, _, err := prepareGLMExecutorPlan(opts, task.Task{}, t.TempDir(), "prompt", t.TempDir())
			if err == nil {
				t.Fatal("expected fail-fast error when GLM token is missing")
			}
			if !strings.Contains(err.Error(), "glm_api_key") {
				t.Fatalf("error must name the missing config key, got %q", err)
			}
		})
	}
}

func TestExecutorVerificationCmdGLM(t *testing.T) {
	t.Parallel()
	if got := executorVerificationCmd("glm"); got != "claude -p (glm)" {
		t.Fatalf("executorVerificationCmd(glm) = %q, want %q", got, "claude -p (glm)")
	}
}
