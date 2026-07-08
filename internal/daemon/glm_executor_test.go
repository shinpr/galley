package daemon

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
