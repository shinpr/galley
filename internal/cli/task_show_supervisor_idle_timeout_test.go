package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	taskpkg "github.com/shinpr/galley/internal/task"
)

// supervisorIdleTimeoutMessage mirrors the operator-facing message the daemon
// writes to task.AttemptError.Message for an exhausted supervisor idle timeout
// (see internal/daemon supervisorIdleTimeoutError.attemptErrorMessage). The
// daemon package owns and tests the exact wording; this fixture only needs a
// representative message so the CLI renderer assertions are meaningful.
const supervisorIdleTimeoutMessage = "supervisor idle timeout: the built-in supervisor subprocess produced no output for the idle-timeout watchdog and was killed " +
	"(supervisor=codex idle_timeout=10m0s). This is a supervisor watchdog failure, not the task execution_policy.timeout_ms expiring. " +
	"Requeue the task, or adjust the daemon --idle-timeout or --supervisor settings."

// writeSupervisorIdleTimeoutFailedTask seeds tasks/failed/task.yaml for a task
// that failed because the built-in supervisor idle timeout exhausted its
// retries.
func writeSupervisorIdleTimeoutFailedTask(t *testing.T, root string) {
	t.Helper()
	taskPath := writeCLITaskYAML(t)
	loaded, err := taskpkg.Load(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Status = "needs_supervisor_review"
	loaded.Attempts = []taskpkg.Attempt{{
		Number:            1,
		StartedAt:         "2026-05-21T09:00:00Z",
		CompletedAt:       "2026-05-21T09:30:00Z",
		ClaudeStatus:      "completed",
		SupervisorVerdict: "supervisor_idle_timeout",
		Summary:           supervisorIdleTimeoutMessage,
		Error: &taskpkg.AttemptError{
			Phase:       "supervisor",
			Kind:        "supervisor_idle_timeout",
			Message:     supervisorIdleTimeoutMessage,
			ArtifactDir: "runs/task-cli-test-1/attempt-1",
		},
	}}
	dst := filepath.Join(root, "tasks", "failed", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := taskpkg.Save(dst, loaded); err != nil {
		t.Fatal(err)
	}
}

// TestTaskShowExplainsSupervisorIdleTimeout exercises AC5: `galley task show`
// on a task failed by an exhausted supervisor idle timeout must make the
// failure understandable without reading daemon logs — failed state, the
// supervisor idle-timeout kind, the supervisor adapter name, the idle-timeout
// duration, the try count, and a short next action. AC4: the rendered output
// must not describe the failure as the task execution_policy.timeout_ms
// expiring.
func TestTaskShowExplainsSupervisorIdleTimeout(t *testing.T) {
	root := t.TempDir()
	writeSupervisorIdleTimeoutFailedTask(t, root)

	stdout, stderr, err := executeCommand("task", "show", "--root", root, "task-cli-test")
	if err != nil {
		t.Fatal(err)
	}
	if stderr != "" {
		t.Fatalf("stderr got %q", stderr)
	}

	for _, want := range []string{
		"state: failed",
		"latest_error_phase: supervisor",
		"latest_error_kind: supervisor_idle_timeout",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("task show output missing %q: %q", want, stdout)
		}
	}

	// The daemon-authored failure message must be surfaced verbatim so the
	// operator sees the explanation without reading daemon logs. The exact
	// wording is owned and tested by the daemon package.
	if !strings.Contains(stdout, supervisorIdleTimeoutMessage) {
		t.Fatalf("task show output must surface the supervisor idle-timeout message: %q", stdout)
	}
}
