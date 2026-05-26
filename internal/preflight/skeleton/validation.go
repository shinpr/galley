package skeleton

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/shinpr/galley/internal/task"
)

func (r acceptanceSkeletonPreflightRun) requireDeclarations(outputs []task.AcceptanceSkeletonOutputDef, noSkeletons []noSkeletonDeclaration) *preflightErr {
	if !r.cfg.IsRequired() || len(outputs) > 0 || len(noSkeletons) > 0 {
		return nil
	}
	return &preflightErr{
		phase:   "acceptance_skeleton_preflight",
		message: "preflight is required but the skeleton creator produced no outputs and no no_skeletons reasons",
	}
}

func (r acceptanceSkeletonPreflightRun) validateNoSkeletonDeclarations(declarations []noSkeletonDeclaration) ([]NoOutput, *preflightErr) {
	noSkeletons := make([]NoOutput, 0, len(declarations))
	for i, ns := range declarations {
		if !r.acIDs[ns.ACID] {
			return nil, &preflightErr{
				phase:   "acceptance_skeleton_provider",
				message: fmt.Sprintf("no_skeletons entry %d ac_id %q does not match any acceptance_criteria.id", i, ns.ACID),
			}
		}
		if strings.TrimSpace(ns.Reason) == "" {
			return nil, &preflightErr{
				phase:   "acceptance_skeleton_provider",
				message: fmt.Sprintf("no_skeletons entry %d for ac_id %q must have a reason", i, ns.ACID),
			}
		}
		noSkeletons = append(noSkeletons, NoOutput{ACID: ns.ACID, Reason: ns.Reason})
	}
	return noSkeletons, nil
}

func (r acceptanceSkeletonPreflightRun) validateDeclarations(declarations []task.AcceptanceSkeletonOutputDef) ([]Output, *preflightErr) {
	outputs := make([]Output, 0, len(declarations))
	for i, decl := range declarations {
		if perr := r.validateOneDeclaration(i, decl); perr != nil {
			return nil, perr
		}
		if perr := r.ensureSkeletonFile(decl); perr != nil {
			return nil, perr
		}
		outputs = append(outputs, Output{
			ACID:                   decl.ACID,
			Path:                   decl.Path,
			Kind:                   decl.Kind,
			Purpose:                decl.Purpose,
			Satisfies:              strings.TrimSpace(decl.Satisfies),
			IntegrationPoint:       strings.TrimSpace(decl.IntegrationPoint),
			ImplementationRequired: decl.ImplementationRequired,
		})
	}
	return outputs, nil
}

func (r acceptanceSkeletonPreflightRun) validateOneDeclaration(i int, decl task.AcceptanceSkeletonOutputDef) *preflightErr {
	if !r.acIDs[decl.ACID] {
		return providerErr("declared output %d ac_id %q does not match any acceptance_criteria.id", i, decl.ACID)
	}
	if perr := validateCreatorDeclaration(i, decl); perr != nil {
		return perr
	}
	if !pathInsideEffective(decl.Path, r.allowed) {
		return providerErr("declared output %d path %q is outside the effective allowed paths", i, decl.Path)
	}
	if pathInsideEffective(decl.Path, r.forbidden) {
		return providerErr("declared output %d path %q is inside scope.forbidden_paths", i, decl.Path)
	}
	if runRel := cleanContainedRel(r.opts.WorkDir, r.opts.RunDir); runRel != "" && pathInsideEffective(decl.Path, []string{runRel}) {
		return providerErr("declared output %d path %q is inside the Galley run evidence directory", i, decl.Path)
	}
	return nil
}

// validateCreatorDeclaration enforces per-output safety checks that are
// independent of other declarations. Multiple outputs may share the same clean
// path because a single skeleton file naturally covers several acceptance
// criteria; the daemon still preserves each entry's AC binding and metadata in
// the preflight result and task acceptance skeleton outputs.
func validateCreatorDeclaration(i int, decl task.AcceptanceSkeletonOutputDef) *preflightErr {
	if reason := unsafeSkeletonOutputPath(decl.Path); reason != "" {
		return providerErr("declared output %d path %q is rejected: %s", i, decl.Path, reason)
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{name: "kind", value: decl.Kind},
		{name: "purpose", value: decl.Purpose},
		{name: "satisfies", value: decl.Satisfies},
		{name: "integration_point", value: decl.IntegrationPoint},
	} {
		if strings.TrimSpace(field.value) == "" {
			return providerErr("declared output %d (ac_id %q) is missing a non-empty %s", i, decl.ACID, field.name)
		}
	}
	return nil
}

func (r acceptanceSkeletonPreflightRun) ensureSkeletonFile(decl task.AcceptanceSkeletonOutputDef) *preflightErr {
	fullPath := filepath.Join(r.opts.WorkDir, decl.Path)
	if !fileExists(fullPath) {
		return &preflightErr{
			phase:   "acceptance_skeleton_creator",
			message: fmt.Sprintf("creator-declared output %s does not exist after the creator command ran", decl.Path),
		}
	}
	if err := ensureRealPathInsideWorktree(r.opts.WorkDir, fullPath); err != nil {
		return &preflightErr{
			phase:   "acceptance_skeleton_creator",
			message: fmt.Sprintf("creator-declared output %s escapes the worktree: %v", decl.Path, err),
		}
	}
	return nil
}

func (r acceptanceSkeletonPreflightRun) checkOutputExistence(outputs []Output) *preflightErr {
	for _, out := range outputs {
		if !fileExists(filepath.Join(r.opts.WorkDir, out.Path)) {
			return &preflightErr{
				phase:   "acceptance_skeleton_creator",
				message: fmt.Sprintf("declared skeleton %s does not exist after preflight", out.Path),
			}
		}
	}
	return nil
}

func (r acceptanceSkeletonPreflightRun) checkACCoverage(outputs []Output, noSkeletons []NoOutput) *preflightErr {
	if !r.cfg.IsRequired() {
		return nil
	}
	covered := map[string]bool{}
	for _, out := range outputs {
		covered[out.ACID] = true
	}
	for _, ns := range noSkeletons {
		covered[ns.ACID] = true
	}
	var missing []string
	for _, ac := range r.opts.Task.AcceptanceCriteria {
		if !covered[ac.ID] {
			missing = append(missing, ac.ID)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return providerErr("required preflight is missing skeleton coverage for AC IDs: %s", strings.Join(missing, ", "))
}

func (r acceptanceSkeletonPreflightRun) validateCreatorWorkspaceChanges(changed []string, declarations []task.AcceptanceSkeletonOutputDef) *preflightErr {
	declared := map[string]bool{}
	for _, decl := range declarations {
		if reason := unsafeSkeletonOutputPath(decl.Path); reason != "" {
			continue
		}
		declared[filepath.Clean(decl.Path)] = true
	}
	for _, rel := range changed {
		clean := filepath.Clean(rel)
		if !declared[clean] {
			return creatorErr("creator changed undeclared path %q; every created or modified file must be listed in outputs", clean)
		}
		if !pathInsideEffective(clean, r.allowed) {
			return creatorErr("creator changed path %q outside the effective allowed paths", clean)
		}
		if pathInsideEffective(clean, r.forbidden) {
			return creatorErr("creator changed path %q inside scope.forbidden_paths", clean)
		}
	}
	changedSet := map[string]bool{}
	for _, rel := range changed {
		changedSet[filepath.Clean(rel)] = true
	}
	for _, decl := range declarations {
		clean := filepath.Clean(decl.Path)
		if !changedSet[clean] {
			return creatorErr("declared output %q was not created or modified by the creator", clean)
		}
	}
	return nil
}

func providerErr(format string, args ...any) *preflightErr {
	return &preflightErr{phase: "acceptance_skeleton_provider", message: fmt.Sprintf(format, args...)}
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func ensureRealPathInsideWorktree(workDir, path string) error {
	realWorkDir, err := filepath.EvalSymlinks(workDir)
	if err != nil {
		return fmt.Errorf("resolve workdir: %w", err)
	}
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("resolve path: %w", err)
	}
	rel, err := filepath.Rel(realWorkDir, realPath)
	if err != nil {
		return fmt.Errorf("compare path containment: %w", err)
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("resolved path %q is outside resolved workdir %q", realPath, realWorkDir)
	}
	return nil
}

func unsafeSkeletonOutputPath(rel string) string {
	if strings.TrimSpace(rel) == "" {
		return "path is empty"
	}
	if filepath.IsAbs(rel) {
		return "path is absolute"
	}
	clean := filepath.Clean(rel)
	if clean == "." {
		return "path refers to the worktree root, not a file"
	}
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "path uses parent-directory traversal"
	}
	return ""
}

func pathInsideEffective(rel string, prefixes []string) bool {
	if rel == "" {
		return false
	}
	clean := filepath.Clean(rel)
	for _, p := range prefixes {
		cp := filepath.Clean(p)
		if cp == "." {
			return true
		}
		if clean == cp || strings.HasPrefix(clean, cp+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// EffectivePreflightPaths resolves preflight allowed paths against task scope.
// When preflight.acceptance_skeleton.allowed_paths is empty the effective set
// equals scope.allowed_paths. Forbidden paths are always inherited from scope.
func EffectivePreflightPaths(t task.Task) ([]string, []string, error) {
	if t.Preflight == nil || t.Preflight.AcceptanceSkeleton == nil {
		return nil, nil, fmt.Errorf("preflight.acceptance_skeleton is not configured")
	}
	cfg := t.Preflight.AcceptanceSkeleton
	allowed := cfg.AllowedPaths
	if len(allowed) == 0 {
		allowed = append([]string{}, t.Scope.AllowedPaths...)
	}
	forbidden := append([]string{}, t.Scope.ForbiddenPaths...)
	return allowed, forbidden, nil
}
