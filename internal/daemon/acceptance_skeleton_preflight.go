// Package daemon — Acceptance Skeleton Preflight stage.
//
// This file owns the dedicated AcceptanceSkeletonPreflight stage that runs
// after inputfiles.Prepare and before the first executor attempt when a task
// sets preflight.acceptance_skeleton.enabled: true. The name is deliberately
// distinct from daemon.Preflight (the startup options resolver in daemon.go)
// per design decision D6.
//
// The stage records its result as runs/<run-id>/preflight_result.json which
// is the runtime source of truth (D2), and updates the running task with
// generated skeleton metadata before the first executor attempt so the
// executor and supervisor share the same AC bindings.
//
// When the section is absent or disabled the stage is skipped entirely so the
// existing daemon flow is unchanged (R1, AC-001).
package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	slashpath "path"
	"path/filepath"
	"strings"

	"github.com/shinpr/galley/internal/profile"
	"github.com/shinpr/galley/internal/task"
)

// normalizeLogicalSkeletonPath converts a worktree-relative skeleton path to a
// slash-based cleaned key suitable for cross-platform deduplication. This
// mirrors task.normalizeLogicalPath (which is unexported); we keep the daemon
// helper local so duplicate-path dedupe in this stage matches the task
// validator's logical-path semantics (`foo/bar` and `foo\bar` collapse to the
// same key on every OS) without exporting the task helper.
func normalizeLogicalSkeletonPath(p string) string {
	if p == "" {
		return ""
	}
	return slashpath.Clean(strings.ReplaceAll(p, "\\", "/"))
}

func jsonDecode(data []byte, v any) error {
	return json.Unmarshal(data, v)
}

// AcceptanceSkeletonResult is the source-of-truth runtime output of the
// AcceptanceSkeletonPreflight stage. It is serialized to
// runs/<run-id>/preflight_result.json.
type AcceptanceSkeletonResult struct {
	Status        string                       `json:"status" yaml:"status"`
	SourceOfTruth bool                         `json:"source_of_truth" yaml:"source_of_truth"`
	Outputs       []AcceptanceSkeletonOutput   `json:"outputs" yaml:"outputs"`
	NoSkeletons   []AcceptanceSkeletonNoOutput `json:"no_skeletons,omitempty" yaml:"no_skeletons,omitempty"`
	Baseline      AcceptanceSkeletonBaseline   `json:"baseline" yaml:"baseline"`
	Error         *AcceptanceSkeletonError     `json:"error,omitempty" yaml:"error,omitempty"`
}

// AcceptanceSkeletonOutput is one declared skeleton file with AC binding and
// generated test skeleton. ImplementationRequired marks outputs the supervisor
// must confirm are implemented rather than left as placeholders.
type AcceptanceSkeletonOutput struct {
	ACID                   string `json:"ac_id" yaml:"ac_id"`
	Path                   string `json:"path" yaml:"path"`
	Kind                   string `json:"kind" yaml:"kind"`
	Purpose                string `json:"purpose" yaml:"purpose"`
	Satisfies              string `json:"satisfies,omitempty" yaml:"satisfies,omitempty"`
	IntegrationPoint       string `json:"integration_point,omitempty" yaml:"integration_point,omitempty"`
	ImplementationRequired bool   `json:"implementation_required" yaml:"implementation_required"`
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
//
// ClaudeBin and CodexBin carry the resolved executor binaries. The acceptance
// skeleton creator selects which one to use from the task implementation
// executor backend (task.executor.cli) so creator runs and implementation
// runs share the task's executor backend configuration. The daemon supervisor
// backend is intentionally not threaded here: supervisor selection is
// independent from acceptance skeleton creator provider selection.
type AcceptanceSkeletonPreflightOptions struct {
	Task      task.Task
	WorkDir   string
	RunDir    string
	Profiles  profile.Bundle
	ClaudeBin string
	CodexBin  string
}

// AcceptanceSkeletonPreflight runs the skeleton creator stage in the prepared
// worktree. It assumes inputfiles.Prepare has already completed. When the task
// only opts in with enabled:true, the built-in creator model creates skeleton
// files and returns the manifest.
func AcceptanceSkeletonPreflight(ctx context.Context, opts AcceptanceSkeletonPreflightOptions) (*AcceptanceSkeletonResult, error) {
	cfg := opts.Task.Preflight
	if cfg == nil || cfg.AcceptanceSkeleton == nil || !cfg.AcceptanceSkeleton.IsEnabled() {
		return nil, nil
	}

	run, err := newAcceptanceSkeletonPreflightRun(opts, cfg.AcceptanceSkeleton)
	if err != nil {
		return preflightFailure(opts.RunDir, "acceptance_skeleton_preflight", err.Error())
	}

	before, snapErr := snapshotPreflightFiles(opts.WorkDir, opts.RunDir)
	if snapErr != nil {
		return preflightFailure(opts.RunDir, "acceptance_skeleton_preflight", snapErr.Error())
	}

	declarations, noSkeletonDecls, perr := resolveSkeletonDeclarations(ctx, opts)
	if perr != nil {
		return preflightFailure(opts.RunDir, perr.phase, perr.message)
	}
	after, snapErr := snapshotPreflightFiles(opts.WorkDir, opts.RunDir)
	if snapErr != nil {
		return preflightFailure(opts.RunDir, "acceptance_skeleton_preflight", snapErr.Error())
	}
	if perr := run.requireDeclarations(declarations, noSkeletonDecls); perr != nil {
		return preflightFailure(opts.RunDir, perr.phase, perr.message)
	}

	noSkeletons, perr := run.validateNoSkeletonDeclarations(noSkeletonDecls)
	if perr != nil {
		return preflightFailure(opts.RunDir, perr.phase, perr.message)
	}
	outputs, perr := run.validateDeclarations(declarations)
	if perr != nil {
		return preflightFailure(opts.RunDir, perr.phase, perr.message)
	}
	if perr := run.validateCreatorWorkspaceChanges(diffPreflightSnapshots(before, after), declarations); perr != nil {
		return preflightFailure(opts.RunDir, perr.phase, perr.message)
	}
	if perr := run.checkOutputExistence(outputs); perr != nil {
		return preflightFailure(opts.RunDir, perr.phase, perr.message)
	}
	if perr := run.checkACCoverage(outputs, noSkeletons); perr != nil {
		return preflightFailure(opts.RunDir, perr.phase, perr.message)
	}

	// Multiple outputs may share the same skeleton path when a single test file
	// covers several acceptance criteria. Dedupe using a slash-normalized
	// logical key (equivalent to task.normalizeLogicalPath) so the baseline
	// records each unique path once on every OS while preserving each AC's
	// output entry above; using a separator-sensitive key would let
	// `internal/foo/foo_test.go` and `internal\foo\foo_test.go` produce
	// duplicate baseline entries on Windows.
	paths := make([]string, 0, len(outputs))
	seenBaselinePaths := map[string]bool{}
	for _, o := range outputs {
		key := normalizeLogicalSkeletonPath(o.Path)
		if seenBaselinePaths[key] {
			continue
		}
		seenBaselinePaths[key] = true
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

type acceptanceSkeletonPreflightRun struct {
	opts      AcceptanceSkeletonPreflightOptions
	cfg       *task.AcceptanceSkeletonConfig
	allowed   []string
	forbidden []string
	acIDs     map[string]bool
}

func newAcceptanceSkeletonPreflightRun(opts AcceptanceSkeletonPreflightOptions, cfg *task.AcceptanceSkeletonConfig) (acceptanceSkeletonPreflightRun, error) {
	allowed, forbidden, err := EffectivePreflightPaths(opts.Task)
	if err != nil {
		return acceptanceSkeletonPreflightRun{}, err
	}
	acIDs := map[string]bool{}
	for _, ac := range opts.Task.AcceptanceCriteria {
		acIDs[ac.ID] = true
	}
	return acceptanceSkeletonPreflightRun{
		opts:      opts,
		cfg:       cfg,
		allowed:   allowed,
		forbidden: forbidden,
		acIDs:     acIDs,
	}, nil
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
