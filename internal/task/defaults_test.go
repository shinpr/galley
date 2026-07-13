package task

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestApplyDefaultsFillsFixedAuthoringValues(t *testing.T) {
	t.Parallel()
	tk := Task{
		ID:   "task-defaults-001",
		Goal: "goal",
		AcceptanceCriteria: []AcceptanceCriterion{{
			ID: "AC1", Text: "text", Verification: "verify", Status: "pending",
		}},
		Scope: Scope{
			CWD: t.TempDir(), AllowedPaths: []string{"."}, Permission: "edit",
		},
		ExecutionPolicy: ExecutionPolicy{TimeoutMS: 1000},
		Worktree:        Worktree{Branch: "agent/task-defaults-001", Path: "../repo.worktrees/task"},
	}
	ApplyDefaults(&tk)
	if tk.Mode != DefaultMode {
		t.Fatalf("mode got %q, want %q", tk.Mode, DefaultMode)
	}
	if tk.Status != StatusDraft {
		t.Fatalf("status got %q, want %q", tk.Status, StatusDraft)
	}
	if !tk.Worktree.Enabled {
		t.Fatal("worktree.enabled should default to true for AFK")
	}
	if !tk.ExecutionPolicy.LoopBudget.Set || tk.ExecutionPolicy.LoopBudget.Count != DefaultLoopBudget {
		t.Fatalf("loop_budget got %#v, want set count=%d", tk.ExecutionPolicy.LoopBudget, DefaultLoopBudget)
	}
}

func TestLoadAndValidateAcceptsMinimalDraft(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cwd := t.TempDir()
	path := filepath.Join(dir, "minimal.yaml")
	body := strings.Join([]string{
		`id: "task-minimal-001"`,
		`goal: "Implement a small change."`,
		`acceptance_criteria:`,
		`  - id: "AC1"`,
		`    text: "observable"`,
		`    verification: "test"`,
		`    status: "pending"`,
		`scope:`,
		`  cwd: ` + strconv.Quote(cwd),
		`  allowed_paths: ["."]`,
		`  forbidden_paths: [".env"]`,
		`  permission: "edit"`,
		`execution_policy:`,
		`  loop_budget: 10`,
		`  timeout_ms: 1000`,
		`worktree:`,
		`  branch: "agent/task-minimal-001"`,
		`  path: "../repo.worktrees/task-minimal-001"`,
		`decisions: []`,
		`risks: []`,
		``,
	}, "\n")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := LoadAndValidate(path)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Valid() {
		t.Fatalf("minimal draft should validate, got %v", result.Errors)
	}
	if result.Task.Mode != DefaultMode {
		t.Fatalf("mode got %q, want %q", result.Task.Mode, DefaultMode)
	}
	if result.Task.Status != StatusDraft {
		t.Fatalf("status got %q, want %q", result.Task.Status, StatusDraft)
	}
	if !result.Task.Worktree.Enabled {
		t.Fatal("worktree should be enabled after defaults")
	}
}

func TestWorkOrderUsesFixedAFKDecisionPolicy(t *testing.T) {
	t.Parallel()
	tk := Task{
		ID:   "task-wo-001",
		Mode: "afk",
		Goal: "goal",
		AcceptanceCriteria: []AcceptanceCriterion{{
			ID: "AC1", Text: "text", Verification: "verify", Status: "pending",
		}},
		Scope: Scope{
			CWD: "/tmp/repo", AllowedPaths: []string{"."}, Permission: "edit",
		},
		ExecutionPolicy: ExecutionPolicy{
			LoopBudget: LoopBudget{Count: 3, Set: true},
			TimeoutMS:  1000,
		},
		Worktree: Worktree{Enabled: true, Branch: "agent/task-wo-001", Path: "../repo.worktrees/task"},
	}
	text := RenderWorkOrder(tk)
	if !strings.Contains(text, "AFK decision policy: `"+DefaultAFKDecisionPolicy+"`") {
		t.Fatalf("work order missing fixed AFK decision policy:\n%s", text)
	}
	if strings.Contains(text, "stop_on_") {
		t.Fatalf("work order must not surface removed stop_on_* knobs:\n%s", text)
	}
}
