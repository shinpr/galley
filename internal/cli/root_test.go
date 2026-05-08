package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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

func TestTaskRequeueText(t *testing.T) {
	taskPath := writeCLITaskYAML(t)
	failedPath := filepath.Join(t.TempDir(), "tasks", "failed", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(failedPath), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.Replace(string(data), `status: "queued"`, `status: "needs_supervisor_review"`, 1))
	if err := os.WriteFile(failedPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, err := executeCommand("task", "requeue", "--reason", "review fixed", failedPath)
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
	taskPath := writeCLITaskYAML(t)
	draftPath := filepath.Join(t.TempDir(), "tasks", "draft", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(draftPath), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.Replace(string(data), `status: "queued"`, `status: "draft"`, 1))
	if err := os.WriteFile(draftPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, err := executeCommand("task", "queue", "--reason", "draft approved for daemon", draftPath)
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

func TestTaskArchiveText(t *testing.T) {
	taskPath := writeCLITaskYAML(t)
	donePath := filepath.Join(t.TempDir(), "tasks", "done", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(donePath), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.Replace(string(data), `status: "queued"`, `status: "accepted"`, 1))
	if err := os.WriteFile(donePath, data, 0o600); err != nil {
		t.Fatal(err)
	}

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

	stdout, stderr, err := executeCommand("task", "requeue", "--output", "json", taskPath)
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
	if !strings.HasPrefix(stdout, "cd ") || !strings.Contains(stdout, `"$(cat /`) {
		t.Fatalf("unexpected shell preview: %q", stdout)
	}
}

func TestClaudeRunUsesFakeClaude(t *testing.T) {
	taskPath := writeCLITaskYAML(t)
	promptPath, schemaPath := cliPromptFiles(t)
	binDir := t.TempDir()
	fakeClaude := filepath.Join(binDir, "claude")
	if err := os.WriteFile(fakeClaude, []byte("#!/bin/sh\necho fake-claude\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)

	stdout, stderr, err := executeCommand("claude", "run", "--system-prompt-file", promptPath, "--json-schema-file", schemaPath, "--timeout-ms", "5000", taskPath)
	if err != nil {
		t.Fatal(err)
	}
	if stderr != "" {
		t.Fatalf("stderr got %q", stderr)
	}

	var payload struct {
		ExitCode int    `json:"exit_code"`
		Stdout   string `json:"stdout"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.ExitCode != 0 || !strings.Contains(payload.Stdout, "fake-claude") {
		t.Fatalf("unexpected run payload: %#v", payload)
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
mode: "hitl"
status: "queued"
goal: "Test CLI."
acceptance_criteria:
  - id: "AC1"
    text: "Runs."
    verification: "go test ./..."
    status: "pending"
scope:
  cwd: "` + dir + `"
  allowed_paths:
    - "internal/task"
  forbidden_paths: []
  permission: "safe-edit"
execution_policy:
  loop_budget: 3
  timeout_ms: 600000
  afk_decision_policy: ""
  stop_on_destructive_operation: true
  stop_on_missing_secret: false
  stop_on_external_service_unavailable: false
worktree:
  enabled: false
  branch: ""
  path: ""
supervisor:
  provider: "codex"
  mode: "review_and_repair"
  approval_required: true
  approval_status: "pending"
  review_iterations: 0
executor:
  cli: "claude"
  model: "opus"
  effort: "high"
  prompt_profile: "codexized-claude-executor-v1"
  prompt_mode: "replace"
  max_budget_usd: 0
  max_turns: 0
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
