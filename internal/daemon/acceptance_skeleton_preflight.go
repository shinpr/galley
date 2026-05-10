// Package daemon — Acceptance Skeleton Preflight stage.
//
// This file owns the dedicated AcceptanceSkeletonPreflight stage that runs
// after inputfiles.Prepare and before the first executor attempt when a task
// sets preflight.acceptance_skeleton.enabled: true. The name is deliberately
// distinct from daemon.Preflight (the startup options resolver in daemon.go)
// per design decision D6.
//
// The stage records its result as runs/<run-id>/preflight_result.json which
// is the runtime source of truth (D2). Skeleton checkpoint commands are run
// after each executor attempt by RunSkeletonCheckpoints which the daemon
// calls during result.Complete; the per-attempt evidence is persisted under
// the attempt directory (D3, D8).
//
// When the section is absent or disabled the stage is skipped entirely so the
// existing daemon flow is unchanged (R1, AC-001).
package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/shinpr/galley/internal/profile"
	"github.com/shinpr/galley/internal/result"
	"github.com/shinpr/galley/internal/runner"
	"github.com/shinpr/galley/internal/task"
)

// CheckpointResult is re-exported from internal/result so the daemon-side
// evidence helpers (WriteCheckpointResults / LoadCheckpointResults) keep a
// single canonical type that matches the JSON evidence shape and the result
// package shell runner.
type CheckpointResult = result.CheckpointResult

func jsonDecode(data []byte, v any) error {
	return json.Unmarshal(data, v)
}

// AcceptanceSkeletonResult is the source-of-truth runtime output of the
// AcceptanceSkeletonPreflight stage. It is serialized to
// runs/<run-id>/preflight_result.json. Attempt-scoped checkpoint command
// results are NOT stored here; they live with each attempt's evidence (D8).
type AcceptanceSkeletonResult struct {
	Status        string                       `json:"status" yaml:"status"`
	SourceOfTruth bool                         `json:"source_of_truth" yaml:"source_of_truth"`
	Outputs       []AcceptanceSkeletonOutput   `json:"outputs" yaml:"outputs"`
	NoSkeletons   []AcceptanceSkeletonNoOutput `json:"no_skeletons,omitempty" yaml:"no_skeletons,omitempty"`
	Baseline      AcceptanceSkeletonBaseline   `json:"baseline" yaml:"baseline"`
	Error         *AcceptanceSkeletonError     `json:"error,omitempty" yaml:"error,omitempty"`
}

// AcceptanceSkeletonOutput is one declared skeleton file with AC binding and
// checkpoint command. ImplementationRequired marks outputs that participate
// in the daemon-side accepted gate (D4). Helper/fixture outputs that support
// a required skeleton may set ImplementationRequired=false.
type AcceptanceSkeletonOutput struct {
	ACID                   string `json:"ac_id" yaml:"ac_id"`
	Path                   string `json:"path" yaml:"path"`
	Kind                   string `json:"kind" yaml:"kind"`
	Purpose                string `json:"purpose" yaml:"purpose"`
	ImplementationRequired bool   `json:"implementation_required" yaml:"implementation_required"`
	CheckpointCommand      string `json:"checkpoint_command" yaml:"checkpoint_command"`
}

// AcceptanceSkeletonNoOutput records an AC the preflight provider declined to
// turn into a skeleton with an explicit reason. Required preflight tasks must
// have either an output or a no_skeletons entry for every AC (Step-2 P2-T3).
type AcceptanceSkeletonNoOutput struct {
	ACID   string `json:"ac_id" yaml:"ac_id"`
	Reason string `json:"reason" yaml:"reason"`
}

// AcceptanceSkeletonBaseline records the post-preflight content hashes that
// the daemon uses to separate skeleton-only diffs from executor progress
// (D5, AC-012, AC-013, AC-014).
type AcceptanceSkeletonBaseline struct {
	SkeletonHashes []SkeletonHash `json:"skeleton_hashes" yaml:"skeleton_hashes"`
	DiffPatchPath  string         `json:"diff_patch_path,omitempty" yaml:"diff_patch_path,omitempty"`
}

// SkeletonHash binds a worktree-relative path to its sha256 content hash
// captured immediately after preflight completed.
type SkeletonHash struct {
	Path   string `json:"path" yaml:"path"`
	SHA256 string `json:"sha256" yaml:"sha256"`
}

// AcceptanceSkeletonError reports preflight failure with phase and message
// so task show / run evidence can surface the reason (AC-007, AC-015).
type AcceptanceSkeletonError struct {
	Phase   string `json:"phase" yaml:"phase"`
	Message string `json:"message" yaml:"message"`
}

// AcceptanceSkeletonPreflightOptions configures one preflight invocation.
type AcceptanceSkeletonPreflightOptions struct {
	Task     task.Task
	WorkDir  string
	RunDir   string
	Profiles profile.Bundle
}

// AcceptanceSkeletonPreflight runs the skeleton creator stage in the
// prepared worktree. It assumes inputfiles.Prepare has already completed.
// The default provider uses the declared Outputs in the task YAML, materializes
// each skeleton file in the worktree (when missing), then captures sha256
// content hashes as the post-preflight baseline (D8).
func AcceptanceSkeletonPreflight(ctx context.Context, opts AcceptanceSkeletonPreflightOptions) (*AcceptanceSkeletonResult, error) {
	cfg := opts.Task.Preflight
	if cfg == nil || cfg.AcceptanceSkeleton == nil || !cfg.AcceptanceSkeleton.IsEnabled() {
		return nil, nil
	}
	_ = ctx

	allowed, forbidden, err := EffectivePreflightPaths(opts.Task)
	if err != nil {
		return preflightFailure(opts.RunDir, "acceptance_skeleton_preflight", err.Error())
	}

	acIDs := map[string]bool{}
	for _, ac := range opts.Task.AcceptanceCriteria {
		acIDs[ac.ID] = true
	}

	// Provider/creator step. When preflight.acceptance_skeleton.creator.command
	// is configured the daemon runs it inside the prepared worktree; the creator
	// is expected to write the skeleton files and emit a manifest describing the
	// AC-linked outputs. Galley then validates the returned paths/AC IDs and
	// their existence against the effective allowed paths. When no creator is
	// configured the daemon falls back to the statically declared outputs in the
	// task YAML.
	declarations, noSkeletonDecls, fromCreator, perr := resolveSkeletonDeclarations(ctx, opts, cfg.AcceptanceSkeleton)
	if perr != nil {
		return preflightFailure(opts.RunDir, perr.phase, perr.message)
	}

	// Required preflight tasks must produce at least one output binding for
	// every AC, or an explicit no_skeletons reason for ACs that are
	// intentionally skipped. Without a declaration the gate cannot bind
	// evidence, so refuse to enter the executor (AC-007).
	if cfg.AcceptanceSkeleton.IsRequired() && len(declarations) == 0 && len(noSkeletonDecls) == 0 {
		return preflightFailure(opts.RunDir, "acceptance_skeleton_preflight",
			"preflight is required but the skeleton creator produced no outputs and no no_skeletons reasons")
	}

	outputs := make([]AcceptanceSkeletonOutput, 0, len(declarations))
	noSkeletons := make([]AcceptanceSkeletonNoOutput, 0, len(noSkeletonDecls))
	for i, ns := range noSkeletonDecls {
		if !acIDs[ns.ACID] {
			return preflightFailure(opts.RunDir, "acceptance_skeleton_provider",
				fmt.Sprintf("no_skeletons entry %d ac_id %q does not match any acceptance_criteria.id", i, ns.ACID))
		}
		if strings.TrimSpace(ns.Reason) == "" {
			return preflightFailure(opts.RunDir, "acceptance_skeleton_provider",
				fmt.Sprintf("no_skeletons entry %d for ac_id %q must have a reason", i, ns.ACID))
		}
		noSkeletons = append(noSkeletons, AcceptanceSkeletonNoOutput{ACID: ns.ACID, Reason: ns.Reason})
	}
	// Phase tag for declaration validation errors: creator-reported manifests
	// are tagged acceptance_skeleton_provider; statically declared outputs were
	// already validated by task.Validate, so failures here are creator-side or
	// environment-side.
	declPhase := "acceptance_skeleton_provider"
	createdPaths := make([]string, 0, len(declarations))
	seenPaths := map[string]int{}
	for i, decl := range declarations {
		if !acIDs[decl.ACID] {
			return preflightFailure(opts.RunDir, declPhase,
				fmt.Sprintf("declared output %d ac_id %q does not match any acceptance_criteria.id", i, decl.ACID))
		}
		// Creator-reported paths are untrusted: reject absolute paths and
		// parent traversal before any allowed-path comparison. The
		// allowed-path set may legitimately contain "." (meaning "anywhere in
		// the worktree"), so without this guard an absolute path like
		// "/etc/passwd" or a traversal like "../../x" would pass the
		// allowed-path check. After this guard, "." correctly means "inside
		// the prepared worktree only" because every surviving path is a clean
		// relative path with no ".." component.
		if fromCreator {
			if reason := unsafeSkeletonOutputPath(decl.Path); reason != "" {
				return preflightFailure(opts.RunDir, declPhase,
					fmt.Sprintf("declared output %d path %q is rejected: %s", i, decl.Path, reason))
			}
			if prev, ok := seenPaths[filepath.Clean(decl.Path)]; ok {
				return preflightFailure(opts.RunDir, declPhase,
					fmt.Sprintf("declared output %d path %q is duplicated (also at output %d)", i, decl.Path, prev))
			}
			seenPaths[filepath.Clean(decl.Path)] = i
			// Creator-reported manifests are untrusted: static outputs[] were
			// already validated by task.Validate, but creator manifest fields
			// have only been JSON-decoded so far. Enforce the same minimum
			// field and command-surface bar here, before any skeleton file is
			// hashed or preflight_result.json is written, so an invalid
			// manifest fails preflight and the executor is never invoked.
			if strings.TrimSpace(decl.Kind) == "" {
				return preflightFailure(opts.RunDir, declPhase,
					fmt.Sprintf("declared output %d (ac_id %q) is missing a non-empty kind", i, decl.ACID))
			}
			if strings.TrimSpace(decl.Purpose) == "" {
				return preflightFailure(opts.RunDir, declPhase,
					fmt.Sprintf("declared output %d (ac_id %q) is missing a non-empty purpose", i, decl.ACID))
			}
			if strings.TrimSpace(decl.CheckpointCommand) == "" {
				return preflightFailure(opts.RunDir, declPhase,
					fmt.Sprintf("declared output %d (ac_id %q) is missing a non-empty checkpoint_command", i, decl.ACID))
			}
			if reason := task.UnsafeCheckpointCommand(decl.CheckpointCommand); reason != "" {
				return preflightFailure(opts.RunDir, declPhase,
					fmt.Sprintf("declared output %d (ac_id %q) checkpoint_command is unsafe: %s", i, decl.ACID, reason))
			}
		}
		if !pathInsideEffective(decl.Path, allowed) {
			return preflightFailure(opts.RunDir, declPhase,
				fmt.Sprintf("declared output %d path %q is outside the effective allowed paths", i, decl.Path))
		}
		if pathInsideEffective(decl.Path, forbidden) {
			return preflightFailure(opts.RunDir, declPhase,
				fmt.Sprintf("declared output %d path %q is inside scope.forbidden_paths", i, decl.Path))
		}
		fullPath := filepath.Join(opts.WorkDir, decl.Path)
		if fromCreator {
			// When a creator command is configured it is responsible for
			// writing every skeleton file it declares. Galley does not
			// auto-materialize creator-declared outputs: a missing file means
			// the creator under-delivered, so fail loudly rather than papering
			// over it with a generated stub.
			if !fileExists(fullPath) {
				return preflightFailure(opts.RunDir, "acceptance_skeleton_creator",
					fmt.Sprintf("creator-declared output %s does not exist after the creator command ran", decl.Path))
			}
		} else if !fileExists(fullPath) {
			// Static outputs[] with no creator configured: Galley materializes
			// the skeleton file from a default template (or the author-supplied
			// template) so the executor has something concrete to complete.
			// Pre-existing files are preserved (idempotent re-run after
			// retries).
			if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
				return preflightFailure(opts.RunDir, "acceptance_skeleton_creator",
					fmt.Sprintf("create skeleton dir for %s: %v", decl.Path, err))
			}
			body := skeletonTemplate(decl)
			if err := os.WriteFile(fullPath, []byte(body), 0o644); err != nil {
				return preflightFailure(opts.RunDir, "acceptance_skeleton_creator",
					fmt.Sprintf("write skeleton %s: %v", decl.Path, err))
			}
			createdPaths = append(createdPaths, decl.Path)
		}
		outputs = append(outputs, AcceptanceSkeletonOutput{
			ACID:                   decl.ACID,
			Path:                   decl.Path,
			Kind:                   decl.Kind,
			Purpose:                decl.Purpose,
			ImplementationRequired: decl.ImplementationRequired,
			CheckpointCommand:      decl.CheckpointCommand,
		})
	}

	// Existence check: every declared output path must be present after the
	// creator/materialization step so the gate can bind hashes and run
	// checkpoint commands.
	for _, out := range outputs {
		if !fileExists(filepath.Join(opts.WorkDir, out.Path)) {
			return preflightFailure(opts.RunDir, "acceptance_skeleton_creator",
				fmt.Sprintf("declared skeleton %s does not exist after preflight", out.Path))
		}
	}

	// For required preflight, every AC must be covered by at least one output
	// or an explicit no_skeletons reason.
	if cfg.AcceptanceSkeleton.IsRequired() {
		covered := map[string]bool{}
		for _, out := range outputs {
			covered[out.ACID] = true
		}
		for _, ns := range noSkeletons {
			covered[ns.ACID] = true
		}
		var missing []string
		for _, ac := range opts.Task.AcceptanceCriteria {
			if !covered[ac.ID] {
				missing = append(missing, ac.ID)
			}
		}
		if len(missing) > 0 {
			return preflightFailure(opts.RunDir, "acceptance_skeleton_provider",
				fmt.Sprintf("required preflight is missing skeleton coverage for AC IDs: %s", strings.Join(missing, ", ")))
		}
	}

	paths := make([]string, 0, len(outputs))
	for _, o := range outputs {
		paths = append(paths, o.Path)
	}
	hashes, err := HashSkeletonFiles(opts.WorkDir, paths)
	if err != nil {
		return preflightFailure(opts.RunDir, "acceptance_skeleton_baseline", err.Error())
	}

	res := &AcceptanceSkeletonResult{
		Status:        "completed",
		SourceOfTruth: true,
		Outputs:       outputs,
		NoSkeletons:   noSkeletons,
		Baseline:      AcceptanceSkeletonBaseline{SkeletonHashes: hashes},
	}
	if err := WritePreflightResult(opts.RunDir, res); err != nil {
		return nil, err
	}
	return res, nil
}

// skeletonTemplate renders a default skeleton body for kinds the creator
// recognizes. Authors can override by supplying a Template field which is
// used verbatim. The default body documents the AC binding and checkpoint
// command and contains the conventional implementation_required marker so
// hollow skeletons can be detected.
func skeletonTemplate(decl task.AcceptanceSkeletonOutputDef) string {
	if strings.TrimSpace(decl.Template) != "" {
		return decl.Template
	}
	header := fmt.Sprintf("Galley acceptance skeleton — DO NOT DELETE\nAC: %s\nKind: %s\nPurpose: %s\nCheckpoint: %s\n", decl.ACID, decl.Kind, decl.Purpose, decl.CheckpointCommand)
	body := "TODO(galley-skeleton): implement the behavior this skeleton verifies. The checkpoint command above must pass without modifying or deleting this skeleton header.\n"
	switch strings.ToLower(decl.Kind) {
	case "go-test", "gotest":
		return "// " + strings.ReplaceAll(header, "\n", "\n// ") + "\n" + "// " + body + "\n" + "package skeleton_pending\n"
	case "shell", "sh", "bash":
		return "#!/bin/sh\n# " + strings.ReplaceAll(header, "\n", "\n# ") + "\n# " + body + "\nexit 1\n"
	default:
		return "# " + strings.ReplaceAll(header, "\n", "\n# ") + "\n# " + body
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// unsafeSkeletonOutputPath rejects creator-reported output paths that could
// escape the prepared worktree or are otherwise not a file path. It returns a
// non-empty reason string when the path is rejected. This is applied before
// the effective allowed-path comparison so an allowed set of "." still means
// "inside the prepared worktree only" rather than "any absolute or traversal
// path".
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

// pathInsideEffective reports whether rel falls inside one of the prefixes
// using the same path semantics as scope.AllowedPaths.
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

// HashSkeletonFiles computes sha256 hashes for each worktree-relative path.
// Results are sorted by path so the baseline is reproducible.
func HashSkeletonFiles(workDir string, paths []string) ([]SkeletonHash, error) {
	out := make([]SkeletonHash, 0, len(paths))
	sorted := append([]string{}, paths...)
	sort.Strings(sorted)
	for _, rel := range sorted {
		full := filepath.Join(workDir, rel)
		data, err := os.ReadFile(full)
		if err != nil {
			return nil, fmt.Errorf("hash skeleton %s: %w", rel, err)
		}
		sum := sha256.Sum256(data)
		out = append(out, SkeletonHash{Path: rel, SHA256: hex.EncodeToString(sum[:])})
	}
	return out, nil
}

// WritePreflightResult persists the runtime source-of-truth file for the
// acceptance skeleton stage.
func WritePreflightResult(runDir string, res *AcceptanceSkeletonResult) error {
	if runDir == "" {
		return fmt.Errorf("run dir is required for preflight result")
	}
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		return fmt.Errorf("create preflight run dir: %w", err)
	}
	return writeJSON(filepath.Join(runDir, "preflight_result.json"), res)
}

// LoadPreflightResult reads the runtime source-of-truth file. Returns (nil,
// nil) when the file does not exist so callers in disabled-preflight tasks
// can read it unconditionally without erroring.
func LoadPreflightResult(runDir string) (*AcceptanceSkeletonResult, error) {
	path := filepath.Join(runDir, "preflight_result.json")
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("stat preflight_result.json: %w", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read preflight_result.json: %w", err)
	}
	var res AcceptanceSkeletonResult
	if err := jsonDecode(data, &res); err != nil {
		return nil, fmt.Errorf("decode preflight_result.json: %w", err)
	}
	return &res, nil
}

// preflightErr carries a phase + message pair so the caller can route it
// through preflightFailure with consistent phase tagging.
type preflightErr struct {
	phase   string
	message string
}

func (e *preflightErr) Error() string { return e.phase + ": " + e.message }

// noSkeletonDeclaration is the internal shape for a creator-reported AC that
// intentionally has no executable skeleton.
type noSkeletonDeclaration struct {
	ACID   string
	Reason string
}

// creatorManifest is the JSON document the skeleton creator command writes to
// the path exported as GALLEY_SKELETON_MANIFEST.
type creatorManifest struct {
	Outputs []struct {
		ACID                   string `json:"ac_id"`
		Path                   string `json:"path"`
		Kind                   string `json:"kind"`
		Purpose                string `json:"purpose"`
		ImplementationRequired bool   `json:"implementation_required"`
		CheckpointCommand      string `json:"checkpoint_command"`
	} `json:"outputs"`
	NoSkeletons []struct {
		ACID   string `json:"ac_id"`
		Reason string `json:"reason"`
	} `json:"no_skeletons"`
}

// resolveSkeletonDeclarations returns the skeleton outputs and no_skeletons
// entries for the preflight stage. When a creator command is configured it is
// run inside the prepared worktree and its manifest is parsed; otherwise the
// statically declared task YAML outputs are used.
func resolveSkeletonDeclarations(ctx context.Context, opts AcceptanceSkeletonPreflightOptions, cfg *task.AcceptanceSkeletonConfig) ([]task.AcceptanceSkeletonOutputDef, []noSkeletonDeclaration, bool, *preflightErr) {
	if cfg.Creator == nil || strings.TrimSpace(cfg.Creator.Command) == "" {
		return cfg.Outputs, nil, false, nil
	}

	allowed, _, _ := EffectivePreflightPaths(opts.Task)
	manifestPath := filepath.Join(opts.RunDir, "preflight_creator_manifest.json")
	_ = os.Remove(manifestPath)
	acIDs := make([]string, 0, len(opts.Task.AcceptanceCriteria))
	for _, ac := range opts.Task.AcceptanceCriteria {
		acIDs = append(acIDs, ac.ID)
	}
	env := append(os.Environ(),
		"GALLEY_SKELETON_MANIFEST="+manifestPath,
		"GALLEY_SKELETON_ACS="+strings.Join(acIDs, ","),
		"GALLEY_SKELETON_ALLOWED_PATHS="+strings.Join(allowed, "\n"),
	)

	timeout := time.Duration(cfg.Creator.TimeoutMS) * time.Millisecond
	runCtx := ctx
	if timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	out, err := runner.RunCommand(runCtx, runner.Command{
		WorkDir: opts.WorkDir,
		Argv:    []string{"/bin/sh", "-c", cfg.Creator.Command},
		Env:     env,
	}, runner.RunOptions{TailBytes: 4096})
	if err != nil {
		return nil, nil, true, &preflightErr{
			phase:   "acceptance_skeleton_creator",
			message: fmt.Sprintf("creator command exited %d: %s", out.ExitCode, strings.TrimSpace(out.Stderr)),
		}
	}
	data, rerr := os.ReadFile(manifestPath)
	if rerr != nil {
		return nil, nil, true, &preflightErr{
			phase:   "acceptance_skeleton_creator",
			message: fmt.Sprintf("creator did not write a manifest at %s: %v", manifestPath, rerr),
		}
	}
	var m creatorManifest
	if jerr := jsonDecode(data, &m); jerr != nil {
		return nil, nil, true, &preflightErr{
			phase:   "acceptance_skeleton_creator",
			message: fmt.Sprintf("creator manifest is not valid JSON: %v", jerr),
		}
	}
	decls := make([]task.AcceptanceSkeletonOutputDef, 0, len(m.Outputs))
	for _, o := range m.Outputs {
		decls = append(decls, task.AcceptanceSkeletonOutputDef{
			ACID:                   o.ACID,
			Path:                   o.Path,
			Kind:                   o.Kind,
			Purpose:                o.Purpose,
			ImplementationRequired: o.ImplementationRequired,
			CheckpointCommand:      o.CheckpointCommand,
		})
	}
	noSkel := make([]noSkeletonDeclaration, 0, len(m.NoSkeletons))
	for _, n := range m.NoSkeletons {
		noSkel = append(noSkel, noSkeletonDeclaration{ACID: n.ACID, Reason: n.Reason})
	}
	return decls, noSkel, true, nil
}

func preflightFailure(runDir, phase, message string) (*AcceptanceSkeletonResult, error) {
	res := &AcceptanceSkeletonResult{
		Status:        "failed",
		SourceOfTruth: true,
		Outputs:       []AcceptanceSkeletonOutput{},
		NoSkeletons:   []AcceptanceSkeletonNoOutput{},
		Baseline:      AcceptanceSkeletonBaseline{SkeletonHashes: []SkeletonHash{}},
		Error:         &AcceptanceSkeletonError{Phase: phase, Message: message},
	}
	if err := WritePreflightResult(runDir, res); err != nil {
		return nil, err
	}
	return res, fmt.Errorf("acceptance skeleton preflight failed: %s", message)
}

// AcceptanceGateInputs are the values the daemon-side accept gate inspects
// before acceptSupervisorVerdict finalizes the task.
type AcceptanceGateInputs struct {
	Required          bool
	Outputs           []AcceptanceSkeletonOutput
	NoSkeletons       []AcceptanceSkeletonNoOutput
	CheckpointResults []CheckpointResult
	AcceptanceIDs     []string
}

// AcceptanceGate enforces D4: an accepted verdict must be downgraded to
// needs_supervisor_review when required skeleton or required-check evidence
// is missing or failed. The first version has no waiver mechanism.
func AcceptanceGate(in AcceptanceGateInputs) (string, bool) {
	if !in.Required {
		var problems []string
		for _, out := range in.Outputs {
			if !out.ImplementationRequired {
				continue
			}
			if !checkpointSatisfied(out, in.CheckpointResults) {
				problems = append(problems, fmt.Sprintf("AC %s skeleton checkpoint %q is missing or failed", out.ACID, out.Command()))
			}
		}
		if len(problems) == 0 {
			return "", true
		}
		return strings.Join(problems, "; "), false
	}

	covered := map[string]bool{}
	for _, out := range in.Outputs {
		covered[out.ACID] = true
	}
	for _, ns := range in.NoSkeletons {
		covered[ns.ACID] = true
	}
	var problems []string
	for _, id := range in.AcceptanceIDs {
		if !covered[id] {
			problems = append(problems, fmt.Sprintf("AC %s has no skeleton output and no no_skeletons reason", id))
		}
	}
	for _, out := range in.Outputs {
		if !out.ImplementationRequired {
			continue
		}
		if !checkpointSatisfied(out, in.CheckpointResults) {
			problems = append(problems, fmt.Sprintf("AC %s skeleton checkpoint %q is missing or failed", out.ACID, out.Command()))
		}
	}
	if len(problems) == 0 {
		return "", true
	}
	return strings.Join(problems, "; "), false
}

// Command returns the checkpoint command for an output, falling back to a
// stable placeholder when the provider declared no command.
func (o AcceptanceSkeletonOutput) Command() string {
	if strings.TrimSpace(o.CheckpointCommand) != "" {
		return o.CheckpointCommand
	}
	return "(no checkpoint command)"
}

func checkpointSatisfied(out AcceptanceSkeletonOutput, results []CheckpointResult) bool {
	for _, r := range results {
		if r.ACID != out.ACID {
			continue
		}
		if r.Command != out.CheckpointCommand {
			continue
		}
		if r.Status == "passed" {
			return true
		}
		return false
	}
	return false
}

// RunSkeletonCheckpoints executes every declared skeleton checkpoint command
// through the existing internal/result verification runner so checkpoint
// evidence shares the same shell handling as required quality checks (D3,
// D8). The returned slice mirrors the order of outputs and is persisted as
// attempt-scoped run evidence by WriteCheckpointResults; supervisor evidence
// receives the latest attempt's slice unchanged.
func RunSkeletonCheckpoints(ctx context.Context, workDir string, outputs []AcceptanceSkeletonOutput, perCommandTimeout time.Duration) []CheckpointResult {
	specs := make([]result.CheckpointSpec, 0, len(outputs))
	for _, out := range outputs {
		specs = append(specs, result.CheckpointSpec{
			ACID:    out.ACID,
			Command: out.CheckpointCommand,
		})
	}
	return result.RunSkeletonCheckpoints(ctx, workDir, specs, perCommandTimeout)
}

// WriteCheckpointResults persists checkpoint results for one attempt under
// attempt-scoped evidence. The file is read by the acceptance gate via
// LoadLatestCheckpointResults to bind the latest attempt's evidence.
func WriteCheckpointResults(attemptDir string, results []CheckpointResult) error {
	if attemptDir == "" {
		return fmt.Errorf("attempt dir is required for checkpoint results")
	}
	if err := os.MkdirAll(attemptDir, 0o700); err != nil {
		return fmt.Errorf("create attempt dir: %w", err)
	}
	if results == nil {
		results = []CheckpointResult{}
	}
	return writeJSON(filepath.Join(attemptDir, "skeleton_checkpoint_results.json"), results)
}

// LoadCheckpointResults reads the attempt-scoped evidence file. Returns nil
// when the file does not exist so callers in tasks without enabled preflight
// can call this unconditionally.
func LoadCheckpointResults(attemptDir string) ([]CheckpointResult, error) {
	path := filepath.Join(attemptDir, "skeleton_checkpoint_results.json")
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("stat skeleton_checkpoint_results.json: %w", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read skeleton_checkpoint_results.json: %w", err)
	}
	var results []CheckpointResult
	if err := jsonDecode(data, &results); err != nil {
		return nil, fmt.Errorf("decode skeleton_checkpoint_results.json: %w", err)
	}
	return results, nil
}

// LoadLatestCheckpointResults inspects only the highest-numbered attempt-N
// directory under runDir and returns its checkpoint results. Evidence is
// attempt-scoped: when the latest attempt directory has no
// skeleton_checkpoint_results.json it returns (nil, latestDir, nil) rather
// than falling back to an older attempt, so a fresh attempt without a
// checkpoint file is treated as missing evidence. Returns (nil, "", nil) when
// runDir has no attempt directory at all.
func LoadLatestCheckpointResults(runDir string) ([]CheckpointResult, string, error) {
	if runDir == "" {
		return nil, "", nil
	}
	entries, err := os.ReadDir(runDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, "", nil
		}
		return nil, "", fmt.Errorf("read run dir: %w", err)
	}
	latestN := -1
	latestDir := ""
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, "attempt-") {
			continue
		}
		var n int
		if _, err := fmt.Sscanf(name, "attempt-%d", &n); err != nil {
			continue
		}
		if n > latestN {
			latestN = n
			latestDir = filepath.Join(runDir, name)
		}
	}
	if latestDir == "" {
		return nil, "", nil
	}
	results, err := LoadCheckpointResults(latestDir)
	if err != nil {
		return nil, latestDir, err
	}
	return results, latestDir, nil
}

// HashesMatchBaseline reports whether the workdir contents at every baseline
// path still match the recorded hashes. Used by the progress detector to
// decide whether a clean diff is genuinely a no-progress attempt or whether
// the executor changed at least one skeleton (D5, AC-012, AC-013, AC-014).
func HashesMatchBaseline(workDir string, baseline AcceptanceSkeletonBaseline) (bool, error) {
	if len(baseline.SkeletonHashes) == 0 {
		return true, nil
	}
	for _, h := range baseline.SkeletonHashes {
		data, err := os.ReadFile(filepath.Join(workDir, h.Path))
		if err != nil {
			// A missing skeleton means the executor altered baseline state;
			// treat that as progress so the no-diff invariant cannot trigger.
			return false, nil
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != h.SHA256 {
			return false, nil
		}
	}
	return true, nil
}
