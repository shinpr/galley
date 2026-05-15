package task

import (
	"strings"
	"testing"
)

func TestValidateAcceptsWellFormedAFKTask(t *testing.T) {
	t.Parallel()
	task := validTask(t)
	result := Validate(task)
	if !result.Valid() {
		t.Fatalf("expected valid task, got errors: %#v", result.Errors)
	}
}

func TestValidateAcceptsOmittedExecutorModel(t *testing.T) {
	t.Parallel()
	task := validTask(t)
	task.Executor.Model = ""

	result := Validate(task)
	if !result.Valid() {
		t.Fatalf("expected omitted executor.model to use CLI default, got errors: %#v", result.Errors)
	}
}

func TestValidateRejectsAbsoluteAllowedPath(t *testing.T) {
	t.Parallel()
	task := validTask(t)
	task.Scope.AllowedPaths = []string{"/tmp"}

	result := Validate(task)
	if result.Valid() {
		t.Fatal("expected invalid task")
	}
}

func TestValidateRequiresAFKWorktree(t *testing.T) {
	t.Parallel()
	task := validTask(t)
	task.Worktree = Worktree{}

	result := Validate(task)
	if result.Valid() {
		t.Fatal("expected invalid AFK task without worktree")
	}
}

func TestValidateStructuralDoesNotStatCWD(t *testing.T) {
	t.Parallel()
	task := validTask(t)
	task.Scope.CWD = "/definitely/missing/galley"

	result := ValidateStructural(task)
	if !result.Valid() {
		t.Fatalf("expected structural validation to ignore missing cwd, got %#v", result.Errors)
	}

	envResult := ValidateEnvironment(task)
	if envResult.Valid() {
		t.Fatal("expected environment validation to reject missing cwd")
	}
}

func TestValidateStructuralRejectsInvalidCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		mutate      func(*Task)
		want        string
		wantWarning string
	}{
		{
			name: "missing loop budget uses default",
			mutate: func(task *Task) {
				task.ExecutionPolicy.LoopBudget = LoopBudget{}
			},
			wantWarning: "execution_policy.loop_budget is empty; defaulting to 10",
		},
		{
			name: "negative loop budget",
			mutate: func(task *Task) {
				task.ExecutionPolicy.LoopBudget = LoopBudget{Count: -1, Set: true}
			},
			want: "execution_policy.loop_budget must be >= 0",
		},
		{
			name: "invalid task id",
			mutate: func(task *Task) {
				task.ID = "../outside"
			},
			want: "id must contain only letters",
		},
		{
			name: "ready status removed",
			mutate: func(task *Task) {
				task.Status = "ready"
			},
			want: "status must be one of",
		},
		{
			name: "invalid mode",
			mutate: func(task *Task) {
				task.Mode = "daemon"
			},
			want: "mode must be one of",
		},
		{
			name: "invalid status",
			mutate: func(task *Task) {
				task.Status = "waiting"
			},
			want: "status must be one of",
		},
		{
			name: "invalid permission",
			mutate: func(task *Task) {
				task.Scope.Permission = "root"
			},
			want: "scope.permission must be one of",
		},
		{
			name: "invalid prompt mode",
			mutate: func(task *Task) {
				task.Executor.PromptMode = "merge"
			},
			want: "executor.prompt_mode must be one of",
		},
		{
			name: "negative explicit executor budget",
			mutate: func(task *Task) {
				task.Executor.MaxBudgetUSD = float64Ptr(-1)
			},
			want: "executor.max_budget_usd cannot be negative",
		},
		{
			name: "duplicate ac id",
			mutate: func(task *Task) {
				task.AcceptanceCriteria = append(task.AcceptanceCriteria, task.AcceptanceCriteria[0])
			},
			want: `acceptance_criteria[1].id "AC1" is duplicated`,
		},
		{
			name: "empty allowed path",
			mutate: func(task *Task) {
				task.Scope.AllowedPaths = []string{""}
			},
			want: "scope.allowed_paths contains an empty path",
		},
		{
			name: "parent allowed path",
			mutate: func(task *Task) {
				task.Scope.AllowedPaths = []string{"../foo"}
			},
			want: `scope.allowed_paths contains parent traversal path "../foo"`,
		},
		{
			name: "parent input file destination",
			mutate: func(task *Task) {
				task.Files = []InputFile{{Source: "plan.md", Destination: "../plan.md"}}
			},
			want: `files[0].destination contains parent traversal path "../plan.md"`,
		},
		{
			name: "parent input file source",
			mutate: func(task *Task) {
				task.Files = []InputFile{{Source: "../plan.md", Destination: "src/plan.md"}}
			},
			want: `files[0].source contains parent traversal path "../plan.md"`,
		},
		{
			name: "input file outside allowed paths",
			mutate: func(task *Task) {
				task.Scope.AllowedPaths = []string{"src"}
				task.Files = []InputFile{{Source: "plan.md", Destination: "docs/plan.md", Commit: false}}
			},
			want: "files[0].destination must be within scope.allowed_paths",
		},
		{
			name: "input file inside forbidden paths",
			mutate: func(task *Task) {
				task.Scope.AllowedPaths = []string{"."}
				task.Scope.ForbiddenPaths = []string{"secrets"}
				task.Files = []InputFile{{Source: "plan.md", Destination: "secrets/plan.md", Commit: false}}
			},
			want: "files[0].destination must not be within scope.forbidden_paths",
		},
		{
			name: "invalid afk policy",
			mutate: func(task *Task) {
				task.Mode = "afk"
				task.ExecutionPolicy.AFKDecisionPolicy = "ask-human"
				task.Worktree = Worktree{Enabled: true, Branch: "agent/test", Path: "../repo.worktrees/test"}
			},
			want: "execution_policy.afk_decision_policy must be one of",
		},
		{
			name: "invalid worktree branch",
			mutate: func(task *Task) {
				task.Mode = "afk"
				task.ExecutionPolicy.AFKDecisionPolicy = "choose-smallest-reversible"
				task.Worktree = Worktree{Enabled: true, Branch: "-bad", Path: "../repo.worktrees/test"}
			},
			want: "worktree.branch must be a valid git branch name",
		},
		{
			name: "source repo internal worktree path",
			mutate: func(task *Task) {
				task.Mode = "afk"
				task.ExecutionPolicy.AFKDecisionPolicy = "choose-smallest-reversible"
				task.Worktree = Worktree{Enabled: true, Branch: "agent/test", Path: "worktrees/test"}
			},
			want: "worktree.path must point to a sibling path outside scope.cwd",
		},
		{
			name: "parent worktree path",
			mutate: func(task *Task) {
				task.Mode = "afk"
				task.ExecutionPolicy.AFKDecisionPolicy = "choose-smallest-reversible"
				task.Worktree = Worktree{Enabled: true, Branch: "agent/test", Path: "../../worktrees/test"}
			},
			want: `worktree.path contains parent traversal path "../../worktrees/test"`,
		},
		{
			name: "afk human decision missing chosen",
			mutate: func(task *Task) {
				task.Mode = "afk"
				task.ExecutionPolicy.AFKDecisionPolicy = "choose-smallest-reversible"
				task.Worktree = Worktree{Enabled: true, Branch: "agent/test", Path: "../repo.worktrees/test"}
				task.Decisions = []Decision{{ID: "D1", NeedsHumanReview: true}}
			},
			want: `decision "D1" needs a chosen value for AFK mode`,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			task := validTask(t)
			tt.mutate(&task)
			result := ValidateStructural(task)
			if tt.want != "" && !contains(result.Errors, tt.want) {
				t.Fatalf("expected error containing %q, got %#v", tt.want, result.Errors)
			}
			if tt.wantWarning != "" && !contains(result.Warnings, tt.wantWarning) {
				t.Fatalf("expected warning containing %q, got %#v", tt.wantWarning, result.Warnings)
			}
			if tt.want == "" && !result.Valid() {
				t.Fatalf("expected valid task, got errors %#v", result.Errors)
			}
		})
	}
}

func TestValidateStructuralAcceptsUnlimitedLoopBudget(t *testing.T) {
	t.Parallel()
	task := validTask(t)
	task.ExecutionPolicy.LoopBudget = LoopBudget{Count: 0, Set: true}

	result := ValidateStructural(task)
	if !result.Valid() {
		t.Fatalf("expected valid task, got %#v", result.Errors)
	}
}

func TestValidateStructuralAcceptsSiblingWorktreePath(t *testing.T) {
	t.Parallel()
	task := validTask(t)
	task.Mode = "afk"
	task.ExecutionPolicy.AFKDecisionPolicy = "choose-smallest-reversible"
	task.Worktree = Worktree{Enabled: true, Branch: "agent/test", Path: "../repo.worktrees/test"}

	result := ValidateStructural(task)
	if !result.Valid() {
		t.Fatalf("expected valid sibling worktree path, got %#v", result.Errors)
	}
}

func TestValidateStructuralWarnsOnWholeRepoAllowedPath(t *testing.T) {
	t.Parallel()
	task := validTask(t)
	task.Scope.AllowedPaths = []string{"./"}

	result := ValidateStructural(task)
	if !result.Valid() {
		t.Fatalf("expected valid task, got %#v", result.Errors)
	}
	if !contains(result.Warnings, "scope.allowed_paths includes the whole repository") {
		t.Fatalf("expected whole repo warning, got %#v", result.Warnings)
	}
}

func TestValidGitBranchNameBoundaries(t *testing.T) {
	t.Parallel()
	tests := []struct {
		branch string
		want   bool
	}{
		{branch: "feature/test", want: true},
		{branch: "release.2026_05-rc1", want: true},
		{branch: "-bad", want: false},
		{branch: "bad..name", want: false},
		{branch: "bad@{name", want: false},
		{branch: "bad.lock", want: false},
		{branch: ".hidden/name", want: false},
		{branch: "bad name", want: false},
		{branch: "bad/", want: false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.branch, func(t *testing.T) {
			t.Parallel()
			if got := validGitBranchName(tt.branch); got != tt.want {
				t.Fatalf("validGitBranchName(%q)=%v, want %v", tt.branch, got, tt.want)
			}
		})
	}
}

func TestRenderWorkOrderIncludesTaskDetails(t *testing.T) {
	t.Parallel()
	task := validTask(t)
	got := RenderWorkOrder(task)

	for _, want := range []string{
		"# Galley Work Order",
		"Task ID: `task-test`",
		"## Acceptance Criteria",
		"- `AC1`: It works.",
		"- allowed paths: `internal/task`",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("work order missing %q:\n%s", want, got)
		}
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if strings.Contains(value, want) {
			return true
		}
	}
	return false
}

func validTask(t *testing.T) Task {
	t.Helper()
	return Task{
		ID:     "task-test",
		Mode:   "afk",
		Status: "queued",
		Goal:   "Test the validator.",
		AcceptanceCriteria: []AcceptanceCriterion{
			{
				ID:           "AC1",
				Text:         "It works.",
				Verification: "go test ./...",
				Status:       "pending",
			},
		},
		Scope: Scope{
			CWD:          t.TempDir(),
			AllowedPaths: []string{"internal/task"},
			Permission:   "edit",
		},
		ExecutionPolicy: ExecutionPolicy{
			LoopBudget:        LoopBudget{Count: 3, Set: true},
			TimeoutMS:         600000,
			AFKDecisionPolicy: "choose-smallest-reversible",
		},
		Worktree: Worktree{
			Enabled: true,
			Branch:  "agent/task-test",
			Path:    "../repo.worktrees/task-test",
		},
		Supervisor: Supervisor{},
		Executor: Executor{
			CLI:           "claude",
			Model:         "opus",
			Effort:        "high",
			PromptProfile: "codexized-claude-executor-v1",
			PromptMode:    "replace",
		},
	}
}

func float64Ptr(v float64) *float64 {
	return &v
}
