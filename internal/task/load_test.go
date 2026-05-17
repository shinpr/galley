package task

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestLoadDecodesKnownYAML(t *testing.T) {
	t.Parallel()
	path := writeTaskYAML(t, "loop_budget: 3")

	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ID != "task-load-test" {
		t.Fatalf("id got %q", loaded.ID)
	}
	if loaded.ExecutionPolicy.LoopBudget.Count != 3 {
		t.Fatalf("loop budget got %#v", loaded.ExecutionPolicy.LoopBudget)
	}
}

func TestLoadDecodesUnlimitedLoopBudget(t *testing.T) {
	t.Parallel()
	path := writeTaskYAML(t, "loop_budget: 0")

	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ExecutionPolicy.LoopBudget.Count != 0 || !loaded.ExecutionPolicy.LoopBudget.Set {
		t.Fatalf("loop budget got %#v", loaded.ExecutionPolicy.LoopBudget)
	}
}

func TestLoadRejectsStringNumberLoopBudget(t *testing.T) {
	t.Parallel()
	path := writeTaskYAML(t, `loop_budget: "3"`)

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "loop_budget must be an integer >= 0") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadRejectsNonScalarLoopBudget(t *testing.T) {
	t.Parallel()
	path := writeTaskYAML(t, "loop_budget: [1, 2, 3]")

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "loop_budget must be an integer >= 0") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadAndValidateReadsValidTask(t *testing.T) {
	t.Parallel()
	path := writeTaskYAML(t, "loop_budget: 3")

	result, err := LoadAndValidate(path)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Valid() {
		t.Fatalf("expected valid task, got %#v", result.Errors)
	}
	if result.Task.ID != "task-load-test" {
		t.Fatalf("id got %q", result.Task.ID)
	}
}

func TestLoadAndValidateDefaultsLoopBudget(t *testing.T) {
	t.Parallel()
	path := writeTaskYAML(t, "")

	result, err := LoadAndValidate(path)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Valid() {
		t.Fatalf("expected valid task, got %#v", result.Errors)
	}
	if result.Task.ExecutionPolicy.LoopBudget.Count != DefaultLoopBudget {
		t.Fatalf("loop budget got %#v", result.Task.ExecutionPolicy.LoopBudget)
	}
}

func TestLoadRejectsUnknownField(t *testing.T) {
	t.Parallel()
	path := writeTaskYAML(t, "loop_budget: 3")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, []byte("unknown_field: true\n")...), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err = Load(path)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "field unknown_field not found") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadWrapsMissingFile(t *testing.T) {
	t.Parallel()

	_, err := Load(filepath.Join(t.TempDir(), "missing.yaml"))
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "read ") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSaveRoundTripsVerificationOutputWithJSONLAndIndentedText(t *testing.T) {
	t.Parallel()
	path := writeTaskYAML(t, "loop_budget: 3")
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	excerpt := "   leading indented text\n" +
		`{"type":"assistant","message":{"content":[{"type":"tool_use","input":{"old_string":"  x\n  y"}}]}}` + "\n" +
		"  less indented tail"
	loaded.Verification.Commands = []VerificationCommand{{
		Cmd:           "claude -p",
		Status:        "passed",
		OutputExcerpt: excerpt,
	}}

	if err := Save(path, loaded); err != nil {
		t.Fatal(err)
	}
	roundTripped, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := roundTripped.Verification.Commands[0].OutputExcerpt; got != excerpt {
		t.Fatalf("output excerpt did not round trip:\nwant %q\n got %q", excerpt, got)
	}
}

func TestSaveOmitsEmptyExecutorModel(t *testing.T) {
	t.Parallel()
	path := writeTaskYAML(t, "loop_budget: 3")
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Executor.Model = ""

	if err := Save(path, loaded); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "model:") {
		t.Fatalf("empty executor.model should be omitted, got:\n%s", string(data))
	}
}

func TestSavePreservesOmittedExecutorMaxBudgetUSD(t *testing.T) {
	t.Parallel()
	path := writeTaskYAML(t, "loop_budget: 3")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.ReplaceAll(string(data), "  max_budget_usd: 0\n", ""))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Executor.MaxBudgetUSD != nil {
		t.Fatalf("omitted executor.max_budget_usd loaded as %#v", loaded.Executor.MaxBudgetUSD)
	}
	loaded.Executor.CLI = "codex"
	loaded.Executor.Model = ""
	loaded.Executor.Effort = "high"
	loaded.Executor.PromptProfile = "codex-executor-v1"

	if err := Save(path, loaded); err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(saved), "max_budget_usd:") {
		t.Fatalf("omitted executor.max_budget_usd should stay omitted, got:\n%s", string(saved))
	}
}

func TestSaveRoundTripsExplicitPositiveExecutorMaxBudgetUSD(t *testing.T) {
	t.Parallel()
	path := writeTaskYAML(t, "loop_budget: 3")
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Executor.MaxBudgetUSD = float64Ptr(4.5)

	if err := Save(path, loaded); err != nil {
		t.Fatal(err)
	}
	roundTripped, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if roundTripped.Executor.MaxBudgetUSD == nil || *roundTripped.Executor.MaxBudgetUSD != 4.5 {
		t.Fatalf("explicit executor.max_budget_usd did not round trip: %#v", roundTripped.Executor.MaxBudgetUSD)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "max_budget_usd: 4.5") {
		t.Fatalf("explicit executor.max_budget_usd should be saved, got:\n%s", string(data))
	}
}

func writeTaskYAML(t *testing.T, loopBudgetLine string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "task.yaml")
	body := `id: "task-load-test"
mode: "afk"
status: "queued"
goal: "Test loading."
acceptance_criteria:
  - id: "AC1"
    text: "Loads."
    verification: "go test ./..."
    status: "pending"
scope:
  cwd: ` + strconv.Quote(dir) + `
  allowed_paths:
    - "internal/task"
  forbidden_paths: []
  permission: "edit"
execution_policy:
  ` + loopBudgetLine + `
  timeout_ms: 600000
  afk_decision_policy: "choose-smallest-reversible"
  stop_on_destructive_operation: true
  stop_on_missing_secret: false
  stop_on_external_service_unavailable: false
worktree:
  enabled: true
  branch: "agent/task-load-test"
  path: "../repo.worktrees/task-load-test"
supervisor:
  review_iterations: 0
executor:
  cli: "claude"
  model: "opus"
  effort: "high"
  prompt_profile: "codexized-claude-executor-v1"
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
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
