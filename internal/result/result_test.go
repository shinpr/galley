package result

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/shinpr/galley/internal/profile"
)

func TestCompleteRecordsAcceptanceGuidanceWithoutExecutingIt(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "proof.txt"), []byte("ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	taskFile := filepath.Join(t.TempDir(), "task.yaml")
	if err := os.WriteFile(taskFile, []byte(`id: "task-result-test"
mode: "afk"
status: "running"
goal: "prove result generation"
acceptance_criteria:
  - id: "AC1"
    text: "proof file exists"
    verification: "Inspect proof.txt or run a focused proof check."
    status: "pending"
scope:
  cwd: `+strconv.Quote(repo)+`
  allowed_paths:
    - "proof.txt"
  forbidden_paths: []
  permission: "edit"
execution_policy:
  loop_budget: 1
  timeout_ms: 60000
  afk_decision_policy: "choose-smallest-reversible"
  stop_on_destructive_operation: true
  stop_on_missing_secret: false
  stop_on_external_service_unavailable: false
worktree:
  enabled: true
  branch: "agent/test"
  path: "../repo.worktrees/test"
supervisor:
  review_iterations: 0
executor:
  cli: "claude"
  model: "opus"
  effort: "high"
  prompt_profile: "test"
  prompt_mode: "replace"
  max_budget_usd: 0
decisions: []
risks: []
attempts: []
verification:
  commands: []
pr:
  url: ""
  status: ""
`), 0o644); err != nil {
		t.Fatal(err)
	}

	output := filepath.Join(t.TempDir(), "result.json")
	generated, err := Complete(context.Background(), CompleteOptions{
		TaskFile: taskFile,
		Output:   output,
		Summary:  "done",
	})
	if err != nil {
		t.Fatal(err)
	}
	if generated.Status != "completed" {
		t.Fatalf("status got %q", generated.Status)
	}
	if generated.AcceptanceCriteria[0].Status != "not_satisfied" {
		t.Fatalf("ac status got %q", generated.AcceptanceCriteria[0].Status)
	}
	if len(generated.Verification) != 0 {
		t.Fatalf("acceptance guidance should not be executed as verification: %#v", generated.Verification)
	}
	if _, err := os.Stat(output); err != nil {
		t.Fatal(err)
	}
}

func TestCompleteWritesEmptyFilesModifiedWhenNoDiff(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	runGit(t, repo, "init")

	taskFile := writeResultTask(t, repo, "test -d .")
	output := filepath.Join(t.TempDir(), "result.json")
	generated, err := Complete(context.Background(), CompleteOptions{
		TaskFile: taskFile,
		Output:   output,
		Summary:  "done",
	})
	if err != nil {
		t.Fatal(err)
	}
	if generated.FilesModified == nil {
		t.Fatal("files_modified must be an empty slice, not nil")
	}
	if len(generated.FilesModified) != 0 {
		t.Fatalf("files_modified got %#v", generated.FilesModified)
	}
}

func TestCompleteRejectsInvalidTaskBeforeVerification(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	taskFile := writeResultTask(t, repo, "")
	output := filepath.Join(t.TempDir(), "result.json")
	_, err := Complete(context.Background(), CompleteOptions{
		TaskFile: taskFile,
		Output:   output,
		Summary:  "done",
	})
	if err == nil {
		t.Fatal("expected invalid task error")
	}
	if _, statErr := os.Stat(output); !os.IsNotExist(statErr) {
		t.Fatalf("output should not be written, stat err=%v", statErr)
	}
}

func TestCompleteRunsRequiredQualityChecks(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	runGit(t, repo, "init")
	if err := os.WriteFile(filepath.Join(repo, "proof.txt"), []byte("ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	taskFile := writeResultTask(t, repo, "test -f proof.txt")
	output := filepath.Join(t.TempDir(), "result.json")
	generated, err := Complete(context.Background(), CompleteOptions{
		TaskFile: taskFile,
		Output:   output,
		Summary:  "done",
		Profiles: profile.Bundle{Quality: &profile.Quality{RequiredChecks: []profile.RequiredCheck{
			{ID: "quality-proof", PreferredCommands: []string{readProofCommand()}, Required: true},
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, verification := range generated.Verification {
		if verification.Command == readProofCommand() && verification.Status == "passed" {
			found = true
		}
	}
	if !found {
		t.Fatalf("required quality check missing: %#v", generated.Verification)
	}
}

func TestCompleteUsesCallerDeadlineInsteadOfStartingAnotherTaskTimeout(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	runGit(t, repo, "init")
	taskFile := writeResultTask(t, repo, "test -d .")
	output := filepath.Join(t.TempDir(), "result.json")
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := Complete(ctx, CompleteOptions{
		TaskFile: taskFile,
		Output:   output,
		Summary:  "done",
		Profiles: profile.Bundle{Quality: &profile.Quality{RequiredChecks: []profile.RequiredCheck{
			{ID: "slow", PreferredCommands: []string{slowCommand()}, Required: true},
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Complete ignored caller deadline, elapsed=%s", elapsed)
	}
}

func writeResultTask(t *testing.T, repo, verification string) string {
	t.Helper()
	taskFile := filepath.Join(t.TempDir(), "task.yaml")
	if err := os.WriteFile(taskFile, []byte(`id: "task-result-test"
mode: "afk"
status: "running"
goal: "prove result generation"
acceptance_criteria:
  - id: "AC1"
    text: "verification passes"
    verification: "`+verification+`"
    status: "pending"
scope:
  cwd: `+strconv.Quote(repo)+`
  allowed_paths:
    - "."
  forbidden_paths: []
  permission: "edit"
execution_policy:
  loop_budget: 1
  timeout_ms: 60000
  afk_decision_policy: "choose-smallest-reversible"
  stop_on_destructive_operation: true
  stop_on_missing_secret: false
  stop_on_external_service_unavailable: false
worktree:
  enabled: true
  branch: "agent/test"
  path: "../repo.worktrees/test"
supervisor:
  review_iterations: 0
executor:
  cli: "claude"
  model: "opus"
  effort: "high"
  prompt_profile: "test"
  prompt_mode: "replace"
  max_budget_usd: 0
decisions: []
risks: []
attempts: []
verification:
  commands: []
pr:
  url: ""
  status: ""
`), 0o644); err != nil {
		t.Fatal(err)
	}
	return taskFile
}

func readProofCommand() string {
	if runtime.GOOS == "windows" {
		return "findstr /C:ok proof.txt"
	}
	return "grep -F ok proof.txt"
}

func slowCommand() string {
	if runtime.GOOS == "windows" {
		return "ping -n 3 127.0.0.1 > NUL"
	}
	return "sleep 2"
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %s\n%s", args, err, output)
	}
}
