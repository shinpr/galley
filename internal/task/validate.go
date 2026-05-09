package task

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

var (
	validModes               = []string{"afk"}
	validStatuses            = []string{"draft", "queued", "running", "needs_supervisor_review", "accepted", "pr_opened", "failed", "closed", "merged", "archived"}
	validPermissions         = []string{"read-only", "edit", "sandbox-full-access"}
	validPromptModes         = []string{"replace", "append"}
	validAFKDecisionPolicies = []string{"choose-smallest-reversible"}
	validTaskIDPattern       = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
)

// Validate runs structural and environment validation for a task.
func Validate(t Task) ValidationResult {
	result := ValidateStructural(t)
	envResult := ValidateEnvironment(t)
	result.Errors = append(result.Errors, envResult.Errors...)
	result.Warnings = append(result.Warnings, envResult.Warnings...)
	return result
}

// ValidateStructural checks task fields without touching the filesystem.
func ValidateStructural(t Task) ValidationResult {
	var result ValidationResult

	require(&result, t.ID != "", "id is required")
	if t.ID != "" {
		require(&result, validTaskIDPattern.MatchString(t.ID), "id must contain only letters, numbers, dot, underscore, and dash")
	}
	require(&result, slices.Contains(validModes, t.Mode), "mode must be one of: %s", strings.Join(validModes, ", "))
	require(&result, slices.Contains(validStatuses, t.Status), "status must be one of: %s", strings.Join(validStatuses, ", "))
	require(&result, t.Goal != "", "goal is required")

	validateAcceptance(&result, t)
	validateInputFiles(&result, t)
	validateScope(&result, t)
	validateExecutionPolicy(&result, t)
	validateWorktree(&result, t)
	validateSupervisor(&result, t)
	validateExecutor(&result, t)

	if t.Mode == "afk" && len(t.Decisions) > 0 {
		for _, d := range t.Decisions {
			if d.NeedsHumanReview && d.Chosen == "" {
				result.Errors = append(result.Errors, fmt.Sprintf("decision %q needs a chosen value for AFK mode", d.ID))
			}
		}
	}

	return result
}

// ValidateEnvironment checks local filesystem assumptions such as scope.cwd.
func ValidateEnvironment(t Task) ValidationResult {
	var result ValidationResult
	if t.Scope.CWD == "" || !filepath.IsAbs(t.Scope.CWD) {
		return result
	}
	stat, err := os.Stat(t.Scope.CWD)
	if err != nil || !stat.IsDir() {
		result.Errors = append(result.Errors, fmt.Sprintf("scope.cwd must exist and be a directory: %s", t.Scope.CWD))
	}
	for i, file := range t.Files {
		if file.Source == "" || !filepath.IsAbs(file.Source) {
			continue
		}
		stat, err := os.Stat(file.Source)
		if err != nil || stat.IsDir() {
			result.Errors = append(result.Errors, fmt.Sprintf("files[%d].source must exist and be a file: %s", i, file.Source))
		}
	}
	return result
}

func validateAcceptance(result *ValidationResult, t Task) {
	require(result, len(t.AcceptanceCriteria) > 0, "acceptance_criteria must contain at least one criterion")
	seen := map[string]bool{}
	for i, ac := range t.AcceptanceCriteria {
		prefix := fmt.Sprintf("acceptance_criteria[%d]", i)
		require(result, ac.ID != "", "%s.id is required", prefix)
		if ac.ID != "" {
			if seen[ac.ID] {
				result.Errors = append(result.Errors, fmt.Sprintf("%s.id %q is duplicated", prefix, ac.ID))
			}
			seen[ac.ID] = true
		}
		require(result, ac.Text != "", "%s.text is required", prefix)
		require(result, ac.Verification != "", "%s.verification is required", prefix)
		if ac.Status == "" {
			result.Warnings = append(result.Warnings, fmt.Sprintf("%s.status is empty; treating as pending", prefix))
		}
	}
}

func validateInputFiles(result *ValidationResult, t Task) {
	for i, file := range t.Files {
		prefix := fmt.Sprintf("files[%d]", i)
		require(result, file.Source != "", "%s.source is required", prefix)
		require(result, file.Destination != "", "%s.destination is required", prefix)
		if file.Destination != "" {
			validateRelativePath(result, prefix+".destination", file.Destination)
		}
		require(result, pathAllowedByScope(file.Destination, t.Scope.AllowedPaths), "%s.destination must be within scope.allowed_paths", prefix)
		require(result, !pathForbiddenByScope(file.Destination, t.Scope.ForbiddenPaths), "%s.destination must not be within scope.forbidden_paths", prefix)
	}
}

func validateScope(result *ValidationResult, t Task) {
	require(result, t.Scope.CWD != "", "scope.cwd is required")
	if t.Scope.CWD != "" {
		if !filepath.IsAbs(t.Scope.CWD) {
			result.Errors = append(result.Errors, "scope.cwd must be an absolute path")
		}
	}
	require(result, len(t.Scope.AllowedPaths) > 0, "scope.allowed_paths must contain at least one relative path")
	for _, p := range t.Scope.AllowedPaths {
		validateRelativePath(result, "scope.allowed_paths", p)
		if p == "." || p == "./" {
			result.Warnings = append(result.Warnings, "scope.allowed_paths includes the whole repository")
		}
	}
	for _, p := range t.Scope.ForbiddenPaths {
		validateRelativePath(result, "scope.forbidden_paths", p)
	}
	require(result, slices.Contains(validPermissions, t.Scope.Permission), "scope.permission must be one of: %s", strings.Join(validPermissions, ", "))
}

func validateExecutionPolicy(result *ValidationResult, t Task) {
	budget := t.ExecutionPolicy.LoopBudget
	if !budget.Set {
		result.Warnings = append(result.Warnings, fmt.Sprintf("execution_policy.loop_budget is empty; defaulting to %d", DefaultLoopBudget))
		budget = LoopBudget{Count: DefaultLoopBudget, Set: true}
	}
	if budget.Infinite && budget.Count != 0 {
		result.Errors = append(result.Errors, "execution_policy.loop_budget cannot be both infinite and counted")
	} else if !budget.Infinite {
		require(result, budget.Count > 0, "execution_policy.loop_budget must be positive")
	}
	require(result, t.ExecutionPolicy.TimeoutMS > 0, "execution_policy.timeout_ms must be positive")
	if t.Mode == "afk" {
		require(result, t.ExecutionPolicy.AFKDecisionPolicy != "", "execution_policy.afk_decision_policy is required for AFK tasks")
		if t.ExecutionPolicy.AFKDecisionPolicy != "" {
			require(result, slices.Contains(validAFKDecisionPolicies, t.ExecutionPolicy.AFKDecisionPolicy), "execution_policy.afk_decision_policy must be one of: %s", strings.Join(validAFKDecisionPolicies, ", "))
		}
	}
}

func validateWorktree(result *ValidationResult, t Task) {
	require(result, t.Worktree.Enabled, "worktree.enabled must be true for AFK tasks")
	require(result, t.Worktree.Branch != "", "worktree.branch is required for AFK tasks")
	if t.Worktree.Branch != "" {
		require(result, validGitBranchName(t.Worktree.Branch), "worktree.branch must be a valid git branch name")
	}
	require(result, t.Worktree.Path != "", "worktree.path is required for AFK tasks")
	if t.Worktree.Path != "" && filepath.IsAbs(t.Worktree.Path) {
		result.Errors = append(result.Errors, "worktree.path must be relative")
	} else if t.Worktree.Path != "" {
		validateWorktreePath(result, t.Worktree.Path)
	}
}

func validateWorktreePath(result *ValidationResult, path string) {
	clean := filepath.Clean(path)
	if clean == ".." || strings.HasPrefix(clean, "../../") {
		result.Errors = append(result.Errors, fmt.Sprintf("worktree.path contains parent traversal path %q", path))
		return
	}
	if !strings.HasPrefix(clean, "../") {
		result.Errors = append(result.Errors, `worktree.path must point to a sibling path outside scope.cwd, for example "../repo.worktrees/task"`)
	}
}

var gitBranchSegmentPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

func validGitBranchName(branch string) bool {
	if branch == "" || strings.HasPrefix(branch, "-") || strings.HasPrefix(branch, "/") || strings.HasSuffix(branch, "/") {
		return false
	}
	if strings.Contains(branch, "..") || strings.ContainsAny(branch, " ~^:?*[\\") || strings.Contains(branch, "@{") {
		return false
	}
	for _, segment := range strings.Split(branch, "/") {
		if segment == "" || segment == "." || strings.HasPrefix(segment, ".") || strings.HasSuffix(segment, ".") || strings.HasSuffix(segment, ".lock") {
			return false
		}
		if !gitBranchSegmentPattern.MatchString(segment) {
			return false
		}
	}
	return true
}

func validateSupervisor(result *ValidationResult, t Task) {
	require(result, t.Supervisor.ReviewIterations >= 0, "supervisor.review_iterations cannot be negative")
}

func validateExecutor(result *ValidationResult, t Task) {
	require(result, t.Executor.CLI == "claude", "executor.cli must be claude for this implementation slice")
	require(result, t.Executor.Model != "", "executor.model is required")
	require(result, t.Executor.Effort != "", "executor.effort is required")
	require(result, t.Executor.PromptProfile != "", "executor.prompt_profile is required")
	if t.Executor.PromptMode == "" {
		result.Warnings = append(result.Warnings, "executor.prompt_mode is empty; defaulting to replace")
	} else {
		require(result, slices.Contains(validPromptModes, t.Executor.PromptMode), "executor.prompt_mode must be one of: %s", strings.Join(validPromptModes, ", "))
	}
	require(result, t.Executor.MaxBudgetUSD >= 0, "executor.max_budget_usd cannot be negative")
}

func pathAllowedByScope(path string, allowed []string) bool {
	if path == "" {
		return false
	}
	cleanPath := filepath.Clean(path)
	for _, allowedPath := range allowed {
		cleanAllowed := filepath.Clean(allowedPath)
		if cleanAllowed == "." {
			return true
		}
		if cleanPath == cleanAllowed || strings.HasPrefix(cleanPath, cleanAllowed+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func pathForbiddenByScope(path string, forbidden []string) bool {
	if path == "" {
		return false
	}
	cleanPath := filepath.Clean(path)
	for _, forbiddenPath := range forbidden {
		cleanForbidden := filepath.Clean(forbiddenPath)
		if cleanForbidden == "." {
			return true
		}
		if cleanPath == cleanForbidden || strings.HasPrefix(cleanPath, cleanForbidden+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func validateRelativePath(result *ValidationResult, field, p string) {
	if p == "" {
		result.Errors = append(result.Errors, fmt.Sprintf("%s contains an empty path", field))
		return
	}
	if filepath.IsAbs(p) {
		result.Errors = append(result.Errors, fmt.Sprintf("%s contains absolute path %q", field, p))
	}
	clean := filepath.Clean(p)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		result.Errors = append(result.Errors, fmt.Sprintf("%s contains parent traversal path %q", field, p))
	}
}

func require(result *ValidationResult, ok bool, format string, args ...any) {
	if ok {
		return
	}
	result.Errors = append(result.Errors, fmt.Sprintf(format, args...))
}
