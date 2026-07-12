package task

import (
	"fmt"
	"os"
	slashpath "path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/shinpr/galley/internal/provider"
)

// normalizeLogicalPath converts a YAML-authored logical path to a slash-based
// cleaned form. Task YAML paths are documented as forward-slash relative paths,
// but authors and the daemon may receive native Windows separators. Comparing
// after slash normalization lets the same validator accept both `../foo/bar`
// and `..\foo\bar` and report parent traversal consistently on every OS. The
// validator does not consult the local filesystem, so logical normalization
// here is safe for `filepath.IsAbs`-rejected absolute paths handled earlier.
func normalizeLogicalPath(p string) string {
	if p == "" {
		return ""
	}
	// `filepath.ToSlash` is a no-op on non-Windows builds, so it cannot make
	// the validator platform-agnostic on its own. Replace backslashes with
	// forward slashes explicitly so YAML authored on Windows is normalized
	// the same way whether validation runs on Unix or Windows. Task YAML
	// paths are documented as logical (slash) relative paths, so collapsing
	// `\` to `/` is the intended canonical form rather than a lossy rewrite.
	return slashpath.Clean(strings.ReplaceAll(p, "\\", "/"))
}

// logicalPathEscapes reports whether a slash-cleaned logical path is `..` or
// starts with `../`. It is separator-independent: a native `filepath.Clean` +
// `strings.HasPrefix(..., "../")` check uses `\` separators on Windows and
// misses `../` prefixes.
func logicalPathEscapes(clean string) bool {
	return clean == ".." || strings.HasPrefix(clean, "../")
}

// isLogicalAbsolutePath reports whether a logical (YAML-authored) task path
// should be treated as absolute on any OS. `filepath.IsAbs` is OS-specific:
// on a Unix host it does not recognize Windows drive-qualified paths such as
// `C:\foo` or `C:/foo`, nor does it always recognize backslash-rooted Windows
// forms like `\foo`. To keep validation platform-independent we additionally
// reject:
//   - slash-rooted paths after replacing backslashes — covers Unix `/foo`,
//     Windows `\foo`, and UNC `\\server\share` (which normalizes to
//     `//server/share`);
//   - Windows drive-qualified paths such as `C:\foo` or `C:/foo`. A separator
//     after the drive colon is required so plain identifiers like `id:val`
//     are not misclassified as absolute paths.
//
// Task YAML paths are documented as logical (slash) relative paths, so these
// forms always indicate a host-foreign absolute that the author should not be
// able to smuggle past the validator regardless of which OS runs it.
func isLogicalAbsolutePath(p string) bool {
	if p == "" {
		return false
	}
	if filepath.IsAbs(p) {
		return true
	}
	slash := strings.ReplaceAll(p, "\\", "/")
	if strings.HasPrefix(slash, "/") {
		return true
	}
	if len(p) >= 3 && p[1] == ':' && (p[2] == '\\' || p[2] == '/') {
		c := p[0]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') {
			return true
		}
	}
	return false
}

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
	validatePreflight(&result, t)

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
		if file.Source != "" && !filepath.IsAbs(file.Source) {
			validateRelativePath(result, prefix+".source", file.Source)
		}
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
	require(result, budget.Count >= 0, "execution_policy.loop_budget must be >= 0; use 0 for unlimited")
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
	// Use isLogicalAbsolutePath so Windows-authored absolute worktree paths
	// (`C:\repo.worktrees`, `\\share\worktrees`, `\foo`) are rejected even on
	// non-Windows hosts. `filepath.IsAbs` is OS-specific and misses these on
	// Unix hosts.
	if t.Worktree.Path != "" && isLogicalAbsolutePath(t.Worktree.Path) {
		result.Errors = append(result.Errors, "worktree.path must be relative")
	} else if t.Worktree.Path != "" {
		validateWorktreePath(result, t.Worktree.Path)
	}
}

func validateWorktreePath(result *ValidationResult, path string) {
	// Worktree paths are authored as logical (slash) relative paths. Normalize
	// to slash form first so the `"../"`/`"../../"` literal checks below behave
	// the same on every OS; native Windows cleaning yields backslash separators
	// that those checks would miss.
	clean := normalizeLogicalPath(path)
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
	require(result, slices.Contains(validExecutorCLIs, t.Executor.CLI), "executor.cli must be one of: %s", strings.Join(validExecutorCLIs, ", "))
	require(result, t.Executor.Effort != "", "executor.effort is required")
	if efforts, ok := provider.EffortsForID(t.Executor.CLI); ok {
		require(result, slices.Contains(efforts, t.Executor.Effort), "executor.effort for %s must be one of: %s", t.Executor.CLI, strings.Join(efforts, ", "))
	}
	require(result, t.Executor.PromptProfile != "", "executor.prompt_profile is required")
	if t.Executor.PromptMode == "" {
		result.Warnings = append(result.Warnings, "executor.prompt_mode is empty; defaulting to replace")
	} else {
		require(result, slices.Contains(validPromptModes, t.Executor.PromptMode), "executor.prompt_mode must be one of: %s", strings.Join(validPromptModes, ", "))
	}
}

func validatePreflight(result *ValidationResult, t Task) {
	if t.Preflight == nil {
		return
	}
	cfg := t.Preflight.AcceptanceSkeleton
	if cfg == nil {
		return
	}
	prefix := "preflight.acceptance_skeleton"
	if cfg.Mode != "" {
		require(result, slices.Contains(validPreflightSkeletonModes, cfg.Mode), "%s.mode must be one of: %s", prefix, strings.Join(validPreflightSkeletonModes, ", "))
	}
	if !cfg.Enabled {
		// When the section is disabled, fields like allowed_paths still validate
		// statically so authors who later toggle enabled get immediate feedback.
	}
	for i, p := range cfg.AllowedPaths {
		field := fmt.Sprintf("%s.allowed_paths[%d]", prefix, i)
		validateRelativePath(result, field, p)
		if p == "" {
			continue
		}
		if filepath.IsAbs(p) {
			continue
		}
		clean := normalizeLogicalPath(p)
		if logicalPathEscapes(clean) {
			continue
		}
		if !pathAllowedByScope(p, t.Scope.AllowedPaths) {
			result.Errors = append(result.Errors, fmt.Sprintf("%s value %q must be inside scope.allowed_paths", field, p))
		}
		if pathForbiddenByScope(p, t.Scope.ForbiddenPaths) {
			result.Errors = append(result.Errors, fmt.Sprintf("%s value %q must not be inside scope.forbidden_paths", field, p))
		}
	}
	validatePreflightOutputs(result, t, cfg, prefix)
}

// validatePreflightOutputs validates each declared skeleton output against
// the AC list and scope. Effective allowed paths are preflight.allowed_paths
// when present, else scope.allowed_paths.
func validatePreflightOutputs(result *ValidationResult, t Task, cfg *AcceptanceSkeletonConfig, prefix string) {
	if len(cfg.Outputs) == 0 {
		return
	}
	acIDs := map[string]bool{}
	for _, ac := range t.AcceptanceCriteria {
		acIDs[ac.ID] = true
	}
	effectiveAllowed := cfg.AllowedPaths
	if len(effectiveAllowed) == 0 {
		effectiveAllowed = t.Scope.AllowedPaths
	}
	// Multiple skeleton outputs may share the same normalized path because a
	// single test file naturally covers more than one acceptance criterion.
	// Each entry's AC binding and metadata (purpose, satisfies, integration
	// point) is preserved separately in the task YAML, work order, and
	// preflight_result.json; per-entry safety checks below remain in force on
	// every declaration regardless of duplication.
	for i, out := range cfg.Outputs {
		field := fmt.Sprintf("%s.outputs[%d]", prefix, i)
		require(result, out.ACID != "", "%s.ac_id is required", field)
		if out.ACID != "" && !acIDs[out.ACID] {
			result.Errors = append(result.Errors, fmt.Sprintf("%s.ac_id %q does not match any acceptance_criteria.id", field, out.ACID))
		}
		require(result, out.Path != "", "%s.path is required", field)
		require(result, out.Kind != "", "%s.kind is required", field)
		require(result, out.Purpose != "", "%s.purpose is required", field)
		if out.Path != "" {
			validateRelativePath(result, field+".path", out.Path)
			if !filepath.IsAbs(out.Path) {
				clean := normalizeLogicalPath(out.Path)
				if !logicalPathEscapes(clean) {
					if !pathAllowedByScope(out.Path, effectiveAllowed) {
						result.Errors = append(result.Errors, fmt.Sprintf("%s.path %q must be inside the effective preflight allowed paths", field, out.Path))
					}
					if pathForbiddenByScope(out.Path, t.Scope.ForbiddenPaths) {
						result.Errors = append(result.Errors, fmt.Sprintf("%s.path %q must not be inside scope.forbidden_paths", field, out.Path))
					}
				}
			}
		}
	}
}

func pathAllowedByScope(path string, allowed []string) bool {
	if path == "" {
		return false
	}
	// Compare in slash form so allowed entries authored as `internal/task` in
	// YAML match descendants regardless of whether the input arrives with `/`
	// or `\` separators. Containment uses a `/`-terminated boundary so
	// same-root strings such as `internal-task` do not match `internal`.
	cleanPath := normalizeLogicalPath(path)
	for _, allowedPath := range allowed {
		cleanAllowed := normalizeLogicalPath(allowedPath)
		if cleanAllowed == "." {
			return true
		}
		if cleanPath == cleanAllowed || strings.HasPrefix(cleanPath, cleanAllowed+"/") {
			return true
		}
	}
	return false
}

func pathForbiddenByScope(path string, forbidden []string) bool {
	if path == "" {
		return false
	}
	cleanPath := normalizeLogicalPath(path)
	for _, forbiddenPath := range forbidden {
		cleanForbidden := normalizeLogicalPath(forbiddenPath)
		if cleanForbidden == "." {
			return true
		}
		if cleanPath == cleanForbidden || strings.HasPrefix(cleanPath, cleanForbidden+"/") {
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
	// Reject every host-foreign absolute form (Unix `/x`, Windows `\x`,
	// drive-qualified `C:\x`/`C:/x`, and UNC `\\srv`) the same way on every
	// OS so YAML authored on one platform cannot smuggle absolute paths past
	// validation on another. See isLogicalAbsolutePath for the recognized
	// forms; `filepath.IsAbs` alone is OS-specific and misses Windows
	// absolutes on Unix hosts.
	if isLogicalAbsolutePath(p) {
		result.Errors = append(result.Errors, fmt.Sprintf("%s contains absolute path %q", field, p))
	}
	// Slash-normalize before checking parent traversal so backslash-cleaned
	// strings on Windows do not silently pass the literal `"../"` prefix test.
	clean := normalizeLogicalPath(p)
	if logicalPathEscapes(clean) {
		result.Errors = append(result.Errors, fmt.Sprintf("%s contains parent traversal path %q", field, p))
	}
}

func require(result *ValidationResult, ok bool, format string, args ...any) {
	if ok {
		return
	}
	result.Errors = append(result.Errors, fmt.Sprintf(format, args...))
}
