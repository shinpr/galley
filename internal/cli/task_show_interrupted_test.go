package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	taskpkg "github.com/shinpr/galley/internal/task"
)

func writeInterruptedTask(t *testing.T, root, artifactDir, message string) {
	t.Helper()
	taskPath := writeCLITaskYAML(t)
	loaded, err := taskpkg.Load(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Status = taskpkg.StatusFailed
	loaded.Attempts = []taskpkg.Attempt{{
		Number:            1,
		ClaudeStatus:      "interrupted",
		SupervisorVerdict: taskpkg.AttemptVerdictNotReviewed,
		Summary:           message,
		Error: &taskpkg.AttemptError{
			Phase:       "executor",
			Kind:        taskpkg.AttemptKindExecutorInterrupted,
			Message:     message,
			ArtifactDir: artifactDir,
		},
	}}
	// The daemon records the interruption recovery action as a task risk
	// mitigation; mirror it here so the JSON consumer assertion is meaningful.
	loaded.Risks = []taskpkg.Risk{{
		ID:                   "executor-interrupted-1",
		Type:                 "partial_verification",
		Detail:               message,
		Mitigation:           "Resolve the interruption cause, then run `galley task requeue` to reuse the retained worktree and start a fresh executor attempt.",
		HumanReviewSuggested: true,
	}}
	dst := filepath.Join(root, "tasks", "failed", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := taskpkg.Save(dst, loaded); err != nil {
		t.Fatal(err)
	}
}

func TestTaskShowSurfacesExecutorInterruption(t *testing.T) {
	root := t.TempDir()
	artifactDir := filepath.Join(root, "runs", "task-cli-test-1", "attempt-1")
	message := "Executor interrupted before supervisor review (provider_api_error). provider status=error_during_execution session_id=sess-9 message=rate limited."
	writeInterruptedTask(t, root, artifactDir, message)

	stdout, stderr, err := executeCommand("task", "show", "--root", root, "task-cli-test")
	if err != nil {
		t.Fatal(err)
	}
	if stderr != "" {
		t.Fatalf("stderr got %q", stderr)
	}
	for _, want := range []string{
		"status: failed",
		"latest_supervisor_verdict: not_reviewed",
		"latest_error_kind: executor_interrupted",
		"session_id=sess-9",
		"latest_error_artifact_dir: " + artifactDir,
		"latest_recovery:",
		"galley task requeue task-cli-test",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout missing %q:\n%s", want, stdout)
		}
	}
}

func TestTaskShowJSONRetainsInterruptionEvidence(t *testing.T) {
	root := t.TempDir()
	artifactDir := filepath.Join(root, "runs", "task-cli-test-1", "attempt-1")
	writeInterruptedTask(t, root, artifactDir, "Executor interrupted before supervisor review (idle_timeout).")

	stdout, _, err := executeCommand("task", "show", "--root", root, "-o", "json", "task-cli-test")
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Task taskpkg.Task `json:"task"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("decode task show json: %v", err)
	}
	if len(payload.Task.Attempts) != 1 {
		t.Fatalf("attempts = %#v", payload.Task.Attempts)
	}
	att := payload.Task.Attempts[0]
	if att.Error == nil || att.Error.Kind != taskpkg.AttemptKindExecutorInterrupted || att.Error.ArtifactDir != artifactDir {
		t.Fatalf("interruption error not retained in json: %#v", att.Error)
	}
	foundRecovery := false
	for _, r := range payload.Task.Risks {
		if strings.Contains(strings.ToLower(r.Mitigation), "requeue") {
			foundRecovery = true
		}
	}
	if !foundRecovery {
		t.Fatalf("expected a requeue recovery mitigation among risks: %#v", payload.Task.Risks)
	}
}
