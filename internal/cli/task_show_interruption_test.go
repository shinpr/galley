package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	taskpkg "github.com/shinpr/galley/internal/task"
)

// The message strings mirror the daemon's interruptionMessage format, which
// internal/daemon owns and tests; this test only checks that task show renders
// what the daemon persists.
func TestTaskShowSurfacesExecutorInterruption(t *testing.T) {
	cases := []struct {
		name        string
		message     string
		wantDetail  []string
		notInOutput []string
	}{
		{
			name:       "claude api failure",
			message:    "Executor interrupted before Supervisor review (reason=claude_result_error); status=error_during_execution; session_id=claude-sess",
			wantDetail: []string{"claude_result_error", "error_during_execution", "claude-sess"},
		},
		{
			name:       "glm api failure",
			message:    "Executor interrupted before Supervisor review (reason=claude_result_error); status=error_during_execution; session_id=glm-sess",
			wantDetail: []string{"claude_result_error", "glm-sess"},
		},
		{
			name:       "codex detail present",
			message:    "Executor interrupted before Supervisor review (reason=codex_turn_failed); code=rate_limit; detail=model overloaded",
			wantDetail: []string{"codex_turn_failed", "rate_limit", "model overloaded"},
		},
		{
			name:        "codex detail absent",
			message:     "Executor interrupted before Supervisor review (reason=codex_turn_failed)",
			wantDetail:  []string{"codex_turn_failed"},
			notInOutput: []string{"code=", "detail="},
		},
		{
			name:        "codex detail unparseable",
			message:     "Executor interrupted before Supervisor review (reason=codex_turn_failed)",
			wantDetail:  []string{"codex_turn_failed"},
			notInOutput: []string{"code=", "detail="},
		},
		{
			name:       "grok non-endturn",
			message:    "Executor interrupted before Supervisor review (reason=grok_non_end_turn); stop_reason=MaxTokens; session_id=grok-sess",
			wantDetail: []string{"grok_non_end_turn", "MaxTokens"},
		},
		{
			name:       "unknown interruption",
			message:    "Executor interrupted before Supervisor review (reason=no_normal_terminal)",
			wantDetail: []string{"no_normal_terminal"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			taskPath := writeCLITaskYAML(t)
			loaded, err := taskpkg.Load(taskPath)
			if err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			artifactDir := filepath.Join(root, "runs", "task-cli-test-1", "attempt-1")
			loaded.Status = "failed"
			loaded.Attempts = []taskpkg.Attempt{{
				Number:            1,
				ClaudeStatus:      "completed",
				SupervisorVerdict: "not_reviewed",
				Summary:           tc.message,
				Error: &taskpkg.AttemptError{
					Phase:       "executor",
					Kind:        "executor_interrupted",
					Message:     tc.message,
					ArtifactDir: artifactDir,
				},
			}}
			dst := filepath.Join(root, "tasks", "failed", "task.yaml")
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := taskpkg.Save(dst, loaded); err != nil {
				t.Fatal(err)
			}

			stdout, _, err := executeCommand("task", "show", "--root", root, "task-cli-test", "-o", "text")
			if err != nil {
				t.Fatalf("task show: %v", err)
			}
			want := []string{
				"latest_error_phase: executor",
				"latest_error_kind: executor_interrupted",
				"latest_executor_interruption: true",
				"latest_recovery: resolve the interruption cause, then run: galley task requeue task-cli-test",
				artifactDir,
			}
			want = append(want, tc.wantDetail...)
			for _, w := range want {
				if !strings.Contains(stdout, w) {
					t.Fatalf("task show output missing %q\n%s", w, stdout)
				}
			}
			for _, n := range tc.notInOutput {
				if strings.Contains(stdout, n) {
					t.Fatalf("task show output must not contain %q\n%s", n, stdout)
				}
			}
		})
	}
}
