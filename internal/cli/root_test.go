package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	taskpkg "github.com/shinpr/galley/internal/task"
	"github.com/shinpr/galley/internal/workspace"
)

func TestVersionOutput(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{{"version"}, {"--version"}} {
		args := args
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()
			stdout, stderr, err := executeCommand(args...)
			if err != nil {
				t.Fatal(err)
			}
			if stderr != "" {
				t.Fatalf("stderr got %q", stderr)
			}
			if !strings.HasPrefix(stdout, "galley dev") {
				t.Fatalf("stdout got %q", stdout)
			}
		})
	}
}

func TestTaskValidateText(t *testing.T) {
	t.Parallel()
	taskPath := writeCLITaskYAML(t)

	stdout, stderr, err := executeCommand("task", "validate", taskPath)
	if err != nil {
		t.Fatal(err)
	}
	if stderr != "" {
		t.Fatalf("stderr got %q", stderr)
	}
	if !strings.Contains(stdout, "valid: task-cli-test") {
		t.Fatalf("stdout got %q", stdout)
	}
}

func TestTaskValidateJSON(t *testing.T) {
	t.Parallel()
	taskPath := writeCLITaskYAML(t)

	stdout, _, err := executeCommand("task", "validate", "--output", "json", taskPath)
	if err != nil {
		t.Fatal(err)
	}

	var payload struct {
		Errors []string `json:"errors"`
		Task   struct {
			ID string `json:"id"`
		} `json:"task"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Task.ID != "task-cli-test" || len(payload.Errors) != 0 {
		t.Fatalf("unexpected payload: %#v", payload)
	}
}

func TestTaskValidateMissingFileReturnsErrorWithoutUsage(t *testing.T) {
	t.Parallel()

	stdout, stderr, err := executeCommand("task", "validate", filepath.Join(t.TempDir(), "missing.yaml"))
	if err == nil {
		t.Fatal("expected error")
	}
	if stdout != "" || stderr != "" {
		t.Fatalf("expected cobra to stay silent, stdout=%q stderr=%q", stdout, stderr)
	}
	if !strings.Contains(err.Error(), "read ") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSchemaGenerateAndCheck(t *testing.T) {
	t.Parallel()
	output := t.TempDir()

	stdout, stderr, err := executeCommand("schema", "generate", "--output", output)
	if err != nil {
		t.Fatal(err)
	}
	if stderr != "" {
		t.Fatalf("stderr got %q", stderr)
	}
	if !strings.Contains(stdout, "generated:") {
		t.Fatalf("stdout got %q", stdout)
	}
	for _, name := range []string{"task.schema.json", "quality.schema.json", "environment.schema.json"} {
		if _, err := os.Stat(filepath.Join(output, name)); err != nil {
			t.Fatalf("generated schema %s missing: %v", name, err)
		}
		if !strings.Contains(stdout, name) {
			t.Fatalf("stdout missing %s: %q", name, stdout)
		}
	}

	stdout, stderr, err = executeCommand("schema", "check", "--path", output)
	if err != nil {
		t.Fatal(err)
	}
	if stderr != "" {
		t.Fatalf("stderr got %q", stderr)
	}
	if !strings.Contains(stdout, "schema up to date:") {
		t.Fatalf("stdout got %q", stdout)
	}
	for _, name := range []string{"task.schema.json", "quality.schema.json", "environment.schema.json"} {
		if !strings.Contains(stdout, name) {
			t.Fatalf("stdout missing %s: %q", name, stdout)
		}
	}

	taskSchema := filepath.Join(output, "task.schema.json")
	data, err := os.ReadFile(taskSchema)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(taskSchema, []byte(strings.ReplaceAll(string(data), "\n", "\r\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := executeCommand("schema", "check", "--path", output); err != nil {
		t.Fatalf("schema check should ignore JSON formatting differences: %v", err)
	}
}

func TestTaskRequeueText(t *testing.T) {
	root := t.TempDir()
	failedPath := setupTaskInState(t, root, "failed", "needs_supervisor_review", nil)
	stdout, stderr, err := executeCommand("task", "requeue", "--root", root, "--reason", "review fixed", failedPath)
	if err != nil {
		t.Fatal(err)
	}
	if stderr != "" {
		t.Fatalf("stderr got %q", stderr)
	}
	if !strings.Contains(stdout, "requeued: task-cli-test") || !strings.Contains(stdout, "moved:") {
		t.Fatalf("stdout got %q", stdout)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(filepath.Dir(failedPath)), "queued", "task.yaml")); err != nil {
		t.Fatalf("queued task missing: %v", err)
	}
}

func TestTaskQueueText(t *testing.T) {
	root := t.TempDir()
	draftPath := setupTaskInState(t, root, "draft", "draft", nil)
	stdout, stderr, err := executeCommand("task", "queue", "--root", root, "--reason", "draft approved for daemon", draftPath)
	if err != nil {
		t.Fatal(err)
	}
	if stderr != "" {
		t.Fatalf("stderr got %q", stderr)
	}
	if !strings.Contains(stdout, "queued: task-cli-test") || !strings.Contains(stdout, "moved:") {
		t.Fatalf("stdout got %q", stdout)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(filepath.Dir(draftPath)), "queued", "task.yaml")); err != nil {
		t.Fatalf("queued task missing: %v", err)
	}
}

func TestTaskListText(t *testing.T) {
	root := t.TempDir()
	setupTaskInState(t, root, "failed", "needs_supervisor_review", func(loaded *taskpkg.Task) {
		loaded.Attempts = []taskpkg.Attempt{{
			Number:            1,
			ClaudeStatus:      "completed",
			SupervisorVerdict: "needs_revision",
			Summary:           "AC1 still missing",
		}}
		loaded.PR.URL = "https://github.com/shinpr/sandbox/pull/123"
	})

	stdout, stderr, err := executeCommand("task", "list", "--root", root)
	if err != nil {
		t.Fatal(err)
	}
	if stderr != "" {
		t.Fatalf("stderr got %q", stderr)
	}
	for _, want := range []string{"failed\tneeds_supervisor_review\ttask-cli-test", "https://github.com/shinpr/sandbox/pull/123", "needs_revision", "AC1 still missing"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout missing %q: %q", want, stdout)
		}
	}
}

func TestTaskShowByIDText(t *testing.T) {
	root := t.TempDir()
	setupTaskInState(t, root, "failed", "failed", func(loaded *taskpkg.Task) {
		loaded.Attempts = []taskpkg.Attempt{{
			Number:            2,
			ClaudeStatus:      "hard_stop",
			SupervisorVerdict: "failed",
			Summary:           "usage limit reached",
			Error: &taskpkg.AttemptError{
				Phase:   "executor",
				Kind:    "timed_out",
				Message: "claude -p timed out",
			},
		}}
		loaded.Risks = []taskpkg.Risk{{
			ID:         "risk-1",
			Type:       "blocked",
			Detail:     "Claude usage limit",
			Mitigation: "Requeue after quota reset.",
		}}
		loaded.Verification.Commands = []taskpkg.VerificationCommand{{
			Cmd:           "pnpm test",
			Status:        "failed",
			OutputExcerpt: "usage limit",
		}}
	})

	stdout, stderr, err := executeCommand("task", "show", "--root", root, "task-cli-test")
	if err != nil {
		t.Fatal(err)
	}
	if stderr != "" {
		t.Fatalf("stderr got %q", stderr)
	}
	for _, want := range []string{"id: task-cli-test", "state: failed", "latest_attempt: 2", "latest_supervisor_verdict: failed", "latest_error_phase: executor", "latest_error_kind: timed_out", "latest_error_message: claude -p timed out", "latest_risk: risk-1 blocked: Claude usage limit", "failed_verification: pnpm test"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout missing %q: %q", want, stdout)
		}
	}
}

func TestTaskShowByIDWithDots(t *testing.T) {
	root := t.TempDir()
	setupTaskInState(t, root, "done", "accepted", func(loaded *taskpkg.Task) {
		loaded.ID = "task.release.1"
	})

	stdout, stderr, err := executeCommand("task", "show", "--root", root, "task.release.1")
	if err != nil {
		t.Fatal(err)
	}
	if stderr != "" {
		t.Fatalf("stderr got %q", stderr)
	}
	if !strings.Contains(stdout, "id: task.release.1") {
		t.Fatalf("stdout got %q", stdout)
	}
}

func TestTaskShowAcceptedTerminalSuppressesPriorFailure(t *testing.T) {
	root := t.TempDir()
	setupTaskInState(t, root, "done", "pr_opened", func(loaded *taskpkg.Task) {
		loaded.PR.URL = "https://example.test/pr/1"
		loaded.PR.Status = "open"
		loaded.Attempts = []taskpkg.Attempt{{
			Number:            3,
			ClaudeStatus:      "failed",
			SupervisorVerdict: "accepted",
			Summary:           "executor retried after transient error",
			Error: &taskpkg.AttemptError{
				Phase:   "executor",
				Kind:    "executor_failed",
				Message: "earlier transient executor failure",
			},
		}}
	})

	stdout, stderr, err := executeCommand("task", "show", "--root", root, "task-cli-test")
	if err != nil {
		t.Fatal(err)
	}
	if stderr != "" {
		t.Fatalf("stderr got %q", stderr)
	}
	for _, forbidden := range []string{
		"latest_claude_status: failed",
		"latest_error_phase: executor",
		"latest_error_kind: executor_failed",
		"latest_error_message: earlier transient executor failure",
	} {
		if strings.Contains(stdout, forbidden) {
			t.Fatalf("accepted task output leaked active failure framing %q:\n%s", forbidden, stdout)
		}
	}
	for _, want := range []string{
		"status: pr_opened",
		"prior_attempt_attempt: 3",
		"prior_attempt_claude_status: failed",
		"prior_attempt_supervisor_verdict: accepted",
		"prior_attempt_error_phase: executor",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("expected %q in output, got:\n%s", want, stdout)
		}
	}

	jsonStdout, _, err := executeCommand("task", "show", "--root", root, "--output", "json", "task-cli-test")
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Task struct {
			Status string `json:"status"`
		} `json:"task"`
	}
	if err := json.Unmarshal([]byte(jsonStdout), &payload); err != nil {
		t.Fatalf("parse json: %v\n%s", err, jsonStdout)
	}
	if payload.Task.Status != "pr_opened" {
		t.Fatalf("json output must reflect accepted terminal status, got %q", payload.Task.Status)
	}
}

// TestTaskShowAcceptedTerminalCoversPRLifecycleStatuses guards the regression
// reviewers flagged in iteration 1: once the daemon's PR cleanup loop moves
// an accepted task from pr_opened to closed or merged, prior failed attempts
// must remain under the prior_attempt_* prefix instead of regressing to the
// active "failed" framing.
func TestTaskShowAcceptedTerminalCoversPRLifecycleStatuses(t *testing.T) {
	for _, terminalStatus := range []string{"closed", "merged"} {
		terminalStatus := terminalStatus
		t.Run(terminalStatus, func(t *testing.T) {
			root := t.TempDir()
			setupTaskInState(t, root, "done", terminalStatus, func(loaded *taskpkg.Task) {
				loaded.PR.URL = "https://example.test/pr/2"
				loaded.PR.Status = terminalStatus
				loaded.Attempts = []taskpkg.Attempt{{
					Number:            2,
					ClaudeStatus:      "failed",
					SupervisorVerdict: "accepted",
					Summary:           "executor retried after transient error",
					Error: &taskpkg.AttemptError{
						Phase:   "executor",
						Kind:    "executor_failed",
						Message: "earlier transient executor failure",
					},
				}}
			})

			stdout, stderr, err := executeCommand("task", "show", "--root", root, "task-cli-test")
			if err != nil {
				t.Fatal(err)
			}
			if stderr != "" {
				t.Fatalf("stderr got %q", stderr)
			}
			for _, forbidden := range []string{
				"latest_claude_status: failed",
				"latest_error_phase: executor",
			} {
				if strings.Contains(stdout, forbidden) {
					t.Fatalf("%s task output leaked active failure framing %q:\n%s", terminalStatus, forbidden, stdout)
				}
			}
			for _, want := range []string{
				"status: " + terminalStatus,
				"prior_attempt_claude_status: failed",
				"prior_attempt_error_phase: executor",
			} {
				if !strings.Contains(stdout, want) {
					t.Fatalf("expected %q in %s task output, got:\n%s", want, terminalStatus, stdout)
				}
			}
		})
	}
}

func TestTaskArchiveText(t *testing.T) {
	root := t.TempDir()
	donePath := setupTaskInState(t, root, "done", "accepted", nil)

	stdout, stderr, err := executeCommand("task", "archive", "--reason", "done", donePath)
	if err != nil {
		t.Fatal(err)
	}
	if stderr != "" {
		t.Fatalf("stderr got %q", stderr)
	}
	if !strings.Contains(stdout, "archived: task-cli-test") || !strings.Contains(stdout, "moved:") {
		t.Fatalf("stdout got %q", stdout)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(filepath.Dir(donePath)), "archived", "task.yaml")); err != nil {
		t.Fatalf("archived task missing: %v", err)
	}
}

func TestTaskArchiveRemovesManagedWorktree(t *testing.T) {
	root := t.TempDir()
	repo := initCLIGitRepo(t)
	worktreePath := filepath.Join(root, "repo.worktrees", "task-cli-test")
	prepared, err := workspace.Prepare(context.Background(), repo, taskpkg.Worktree{
		Enabled: true,
		Branch:  "agent/task-cli-test",
		Path:    worktreePath,
	}, workspace.Options{})
	if err != nil {
		t.Fatalf("prepare worktree: %v", err)
	}
	if !prepared.WorktreeCreated {
		t.Fatalf("expected worktree to be created: %+v", prepared)
	}
	donePath := setupTaskInState(t, root, "done", "accepted", func(loaded *taskpkg.Task) {
		loaded.Scope.CWD = repo
		loaded.Worktree.Enabled = true
		loaded.Worktree.Branch = "agent/task-cli-test"
		loaded.Worktree.Path = worktreePath
	})

	stdout, stderr, err := executeCommand("task", "archive", "--output", "json", "--reason", "done", donePath)
	if err != nil {
		t.Fatal(err)
	}
	if stderr != "" {
		t.Fatalf("stderr got %q", stderr)
	}
	var payload struct {
		To              string `json:"to"`
		WorktreeCleanup *struct {
			Path           string `json:"path"`
			Removed        bool   `json:"removed"`
			AlreadyMissing bool   `json:"already_missing"`
		} `json:"worktree_cleanup"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("parse json: %v\n%s", err, stdout)
	}
	if payload.WorktreeCleanup == nil {
		t.Fatalf("json output missing worktree_cleanup: %s", stdout)
	}
	if payload.WorktreeCleanup.Path != worktreePath || !payload.WorktreeCleanup.Removed || payload.WorktreeCleanup.AlreadyMissing {
		t.Fatalf("worktree cleanup got %+v", payload.WorktreeCleanup)
	}
	if _, err := os.Stat(worktreePath); !os.IsNotExist(err) {
		t.Fatalf("worktree should be removed, err=%v", err)
	}
	if _, err := os.Stat(payload.To); err != nil {
		t.Fatalf("archived task missing: %v", err)
	}
}

func setupTaskInState(t *testing.T, root, state, status string, modify func(*taskpkg.Task)) string {
	t.Helper()
	path := filepath.Join(root, "tasks", state, "task.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	loaded, err := taskpkg.Load(writeCLITaskYAML(t))
	if err != nil {
		t.Fatal(err)
	}
	loaded.Status = status
	if modify != nil {
		modify(&loaded)
	}
	if err := taskpkg.Save(path, loaded); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestTaskRequeueJSON(t *testing.T) {
	taskPath := writeCLITaskYAML(t)
	data, err := os.ReadFile(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.Replace(string(data), `status: "queued"`, `status: "failed"`, 1))
	if err := os.WriteFile(taskPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	stdout, stderr, err := executeCommand("task", "requeue", "--root", root, "--output", "json", taskPath)
	if err != nil {
		t.Fatal(err)
	}
	if stderr != "" {
		t.Fatalf("stderr got %q", stderr)
	}
	var payload struct {
		Task struct {
			Status string `json:"status"`
		} `json:"task"`
		From string `json:"from"`
		To   string `json:"to"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Task.Status != "queued" || payload.From == "" || payload.To == "" {
		t.Fatalf("unexpected payload: %#v", payload)
	}
}

func TestProfileValidateQuality(t *testing.T) {
	t.Parallel()
	path, err := filepath.Abs("../../examples/quality-default.yaml")
	if err != nil {
		t.Fatal(err)
	}
	stdout, stderr, err := executeCommand("profile", "validate", "--kind", "quality", path)
	if err != nil {
		t.Fatal(err)
	}
	if stderr != "" {
		t.Fatalf("stderr got %q", stderr)
	}
	if !strings.Contains(stdout, "valid: quality default") {
		t.Fatalf("stdout got %q", stdout)
	}
}

func TestProfileResolveJSON(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	repo := t.TempDir()
	stdout, stderr, err := executeCommand("profile", "resolve", "--root", root, "--cwd", repo, "--output", "json")
	if err != nil {
		t.Fatal(err)
	}
	if stderr != "" {
		t.Fatalf("stderr got %q", stderr)
	}
	var payload struct {
		Root                   string `json:"root"`
		CWD                    string `json:"cwd"`
		RepoKey                string `json:"repo_key"`
		QualityProfileFile     string `json:"quality_profile_file"`
		EnvironmentProfileFile string `json:"environment_profile_file"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Root != root || payload.CWD != repo || payload.RepoKey == "" || payload.QualityProfileFile == "" || payload.EnvironmentProfileFile == "" {
		t.Fatalf("payload got %#v", payload)
	}
}

func TestProfileResolveMkdir(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	repo := t.TempDir()
	stdout, stderr, err := executeCommand("profile", "resolve", "--root", root, "--cwd", repo, "--mkdir", "--output", "json")
	if err != nil {
		t.Fatal(err)
	}
	if stderr != "" {
		t.Fatalf("stderr got %q", stderr)
	}
	var payload struct {
		QualityProfileFile string `json:"quality_profile_file"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(payload.QualityProfileFile)); err != nil {
		t.Fatalf("profile dir missing: %v", err)
	}
}

func TestClaudeArgsJSON(t *testing.T) {
	t.Parallel()
	taskPath := writeCLITaskYAML(t)

	promptPath, schemaPath := cliPromptFiles(t)
	stdout, stderr, err := executeCommand("claude", "args", "--output", "json", "--system-prompt-file", promptPath, "--json-schema-file", schemaPath, taskPath)
	if err != nil {
		t.Fatal(err)
	}
	if stderr != "" {
		t.Fatalf("stderr got %q", stderr)
	}

	var payload struct {
		WorkDir string   `json:"work_dir"`
		Argv    []string `json:"argv"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.WorkDir == "" {
		t.Fatal("work_dir is empty")
	}
	if len(payload.Argv) == 0 || payload.Argv[0] != "claude" {
		t.Fatalf("unexpected argv: %#v", payload.Argv)
	}
	if strings.Contains(strings.Join(payload.Argv, "\x00"), "$(cat ") {
		t.Fatalf("json argv must not include shell substitution: %#v", payload.Argv)
	}
}

func TestClaudeArgsIncludesProfiles(t *testing.T) {
	t.Parallel()
	taskPath := writeCLITaskYAML(t)
	promptPath, schemaPath := cliPromptFiles(t)
	qualityPath, err := filepath.Abs("../../examples/quality-default.yaml")
	if err != nil {
		t.Fatal(err)
	}
	envPath, err := filepath.Abs("../../examples/environment-local.yaml")
	if err != nil {
		t.Fatal(err)
	}

	stdout, stderr, err := executeCommand("claude", "args", "--output", "json", "--system-prompt-file", promptPath, "--json-schema-file", schemaPath, "--quality-profile-file", qualityPath, "--environment-profile-file", envPath, taskPath)
	if err != nil {
		t.Fatal(err)
	}
	if stderr != "" {
		t.Fatalf("stderr got %q", stderr)
	}
	if !strings.Contains(stdout, "Quality Profile") || !strings.Contains(stdout, "Environment Profile") {
		t.Fatalf("profile context missing from command plan: %q", stdout)
	}
}

func TestClaudeArgsRejectsInvalidProfile(t *testing.T) {
	t.Parallel()
	taskPath := writeCLITaskYAML(t)
	promptPath, schemaPath := cliPromptFiles(t)
	qualityPath := filepath.Join(t.TempDir(), "quality.yaml")
	body := `id: bad
required_checks:
  - id: tests
    preferred_commands: []
    required: true
review_dimensions: []
pass_policy:
  min_score: 85
`
	if err := os.WriteFile(qualityPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	_, _, err := executeCommand("claude", "args", "--output", "json", "--system-prompt-file", promptPath, "--json-schema-file", schemaPath, "--quality-profile-file", qualityPath, taskPath)
	if err == nil {
		t.Fatal("expected invalid profile error")
	}
	if !strings.Contains(err.Error(), "invalid quality profile") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestClaudeArgsShell(t *testing.T) {
	t.Parallel()
	taskPath := writeCLITaskYAML(t)

	promptPath, schemaPath := cliPromptFiles(t)
	stdout, stderr, err := executeCommand("claude", "args", "--system-prompt-file", promptPath, "--json-schema-file", schemaPath, taskPath)
	if err != nil {
		t.Fatal(err)
	}
	if stderr != "" {
		t.Fatalf("stderr got %q", stderr)
	}
	if !strings.HasPrefix(stdout, "cd ") || !strings.Contains(stdout, `--system-prompt "$(cat `) || !strings.Contains(stdout, `--json-schema `) {
		t.Fatalf("unexpected shell preview: %q", stdout)
	}
	if strings.Contains(stdout, `--json-schema "$(cat `) || strings.Contains(stdout, "$schema") {
		t.Fatalf("shell preview must inline Claude-compatible schema: %q", stdout)
	}
}

func executeCommand(args ...string) (string, string, error) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd := NewRootCommand()
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return stdout.String(), stderr.String(), err
}

func writeCLITaskYAML(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "task.yaml")
	body := `id: "task-cli-test"
mode: "afk"
status: "queued"
goal: "Test CLI."
acceptance_criteria:
  - id: "AC1"
    text: "Runs."
    verification: "go test ./..."
    status: "pending"
scope:
  cwd: ` + strconv.Quote(dir) + `
  allowed_paths:
    - "internal/task"
  forbidden_paths: []
  permission: "edit"
execution_policy:
  loop_budget: 3
  timeout_ms: 600000
  afk_decision_policy: "choose-smallest-reversible"
  stop_on_destructive_operation: true
  stop_on_missing_secret: false
  stop_on_external_service_unavailable: false
worktree:
  enabled: true
  branch: "agent/task-cli-test"
  path: "../repo.worktrees/task-cli-test"
supervisor:
  review_iterations: 0
executor:
  cli: "claude"
  model: "opus"
  effort: "high"
  prompt_profile: "codexized-claude-executor-v1"
  prompt_mode: "replace"
decisions: []
risks: []
attempts: []
verification:
  commands: []
pr:
  url: ""
  status: ""
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func initCLIGitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary required")
	}
	repo := t.TempDir()
	runCLIGit(t, repo, "init")
	runCLIGit(t, repo, "config", "user.email", "test@example.com")
	runCLIGit(t, repo, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runCLIGit(t, repo, "add", "README.md")
	runCLIGit(t, repo, "commit", "-m", "initial")
	return repo
}

func runCLIGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, output)
	}
}

func cliPromptFiles(t *testing.T) (string, string) {
	t.Helper()
	promptPath, err := filepath.Abs("../../prompts/claude-executor-full.md")
	if err != nil {
		t.Fatal(err)
	}
	schemaPath, err := filepath.Abs("../../schemas/claude-result.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	return promptPath, schemaPath
}
