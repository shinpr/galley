package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	taskpkg "github.com/shinpr/galley/internal/task"
)

func TestTaskShowExecutorInterruptionRendering(t *testing.T) {
	root := t.TempDir()
	artifactDir := filepath.Join(root, "runs", "task-cli-test-1", "attempt-1")
	interruptMessage := "Executor interrupted before Supervisor review (reason=claude_result_error); status=error_during_execution; session_id=claude-sess"

	cases := []struct {
		name             string
		attempt          taskpkg.Attempt
		wantInterruption bool
		wantDetail       []string
	}{
		{
			name: "executor interruption",
			attempt: taskpkg.Attempt{
				Number:            1,
				ClaudeStatus:      "completed",
				SupervisorVerdict: "not_reviewed",
				Summary:           interruptMessage,
				Error: &taskpkg.AttemptError{
					Phase:       "executor",
					Kind:        "executor_interrupted",
					Message:     interruptMessage,
					ArtifactDir: artifactDir,
				},
			},
			wantInterruption: true,
			wantDetail:       []string{"claude_result_error", "error_during_execution", "claude-sess"},
		},
		{
			name: "ordinary supervisor failure",
			attempt: taskpkg.Attempt{
				Number:            1,
				ClaudeStatus:      "completed",
				SupervisorVerdict: "supervisor_failed",
				Summary:           "supervisor evaluation failed",
				Error: &taskpkg.AttemptError{
					Phase:       "supervisor",
					Kind:        "supervisor_failed",
					Message:     "supervisor crashed",
					ArtifactDir: artifactDir,
				},
			},
			wantInterruption: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			writeFailedTaskWithAttempt(t, root, tc.attempt)
			stdout, _, err := executeCommand("task", "show", "--root", root, "task-cli-test", "-o", "text")
			if err != nil {
				t.Fatalf("task show: %v", err)
			}
			assertInterruptionRendering(t, stdout, interruptionRendering{
				WantInterruption: tc.wantInterruption,
				ArtifactDir:      artifactDir,
				WantDetail:       tc.wantDetail,
			})
		})
	}
}

func writeFailedTaskWithAttempt(t *testing.T, root string, attempt taskpkg.Attempt) {
	t.Helper()
	taskPath := writeCLITaskYAML(t)
	loaded, err := taskpkg.Load(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Status = "failed"
	loaded.Attempts = []taskpkg.Attempt{attempt}
	dst := filepath.Join(root, "tasks", "failed", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := taskpkg.Save(dst, loaded); err != nil {
		t.Fatal(err)
	}
}

// interruptionRendering is what `task show` must (or must not) render for one
// failed attempt.
type interruptionRendering struct {
	WantInterruption bool
	ArtifactDir      string
	WantDetail       []string
}

func assertInterruptionRendering(t *testing.T, stdout string, want interruptionRendering) {
	t.Helper()
	markers := []string{
		"latest_executor_interruption: true",
		"latest_recovery: resolve the interruption cause, then run: galley task requeue task-cli-test",
	}
	if !want.WantInterruption {
		for _, marker := range markers {
			if strings.Contains(stdout, marker) {
				t.Fatalf("ordinary failure must not render %q\n%s", marker, stdout)
			}
		}
		return
	}
	expected := append([]string{
		"latest_error_phase: executor",
		"latest_error_kind: executor_interrupted",
		want.ArtifactDir,
	}, markers...)
	expected = append(expected, want.WantDetail...)
	for _, w := range expected {
		if !strings.Contains(stdout, w) {
			t.Fatalf("task show output missing %q\n%s", w, stdout)
		}
	}
}
