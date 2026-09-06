package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/shinpr/galley/internal/pathutil"
	setuppreflight "github.com/shinpr/galley/internal/preflight/setup"
	skeletonpreflight "github.com/shinpr/galley/internal/preflight/skeleton"
	"github.com/shinpr/galley/internal/profile"
	"github.com/shinpr/galley/internal/runartifact"
	"github.com/shinpr/galley/internal/task"
	"github.com/shinpr/galley/prompts"
	"github.com/shinpr/galley/schemas"
)

type preflightReuseRecord struct {
	InputHash string `json:"input_hash"`
	SourceRun string `json:"source_run"`
}

func preflightInputsMatch(dir, phase, key string) bool {
	if key == "" {
		return false
	}
	record, err := runartifact.Read[preflightReuseRecord](dir, phase+"_reuse.json")
	return err == nil && record != nil && record.InputHash == key
}

func recordPreflightInputs(runDir, phase, key, source string) error {
	if key == "" {
		return nil
	}
	return runartifact.Write(runDir, phase+"_reuse.json", preflightReuseRecord{InputHash: key, SourceRun: source})
}

// preflightInputKey excludes runtime results and binds reuse to the stage's current inputs.
// preflightInputSources are the current inputs a preflight stage is keyed on.
type preflightInputSources struct {
	Loaded   task.Task
	Profiles profile.Bundle
	Prepared claimedWorkspace
	Executor task.Executor
}

func preflightInputKey(phase string, sources preflightInputSources) string {
	loaded, profiles, prepared, executor := sources.Loaded, sources.Profiles, sources.Prepared, sources.Executor
	acceptance := append([]task.AcceptanceCriterion(nil), loaded.AcceptanceCriteria...)
	for i := range acceptance {
		acceptance[i].Status = ""
		acceptance[i].Verification = skeletonpreflight.VerificationWithoutSkeleton(acceptance[i].Verification)
	}
	var preflight *task.Preflight
	if loaded.Preflight != nil && loaded.Preflight.AcceptanceSkeleton != nil {
		preflight = &task.Preflight{AcceptanceSkeleton: &task.AcceptanceSkeletonConfig{Enabled: loaded.Preflight.AcceptanceSkeleton.Enabled}}
	}
	inputs := map[string]any{
		"version": 1, "phase": phase, "id": loaded.ID, "mode": loaded.Mode, "goal": loaded.Goal,
		"acceptance": acceptance, "scope": loaded.Scope, "execution_policy": loaded.ExecutionPolicy,
		"executor": executor, "preflight": preflight, "profiles": profiles,
		"workdir": pathutil.CleanPhysical(prepared.CWD), "branch": prepared.Branch,
		"files": loaded.Files, "input_files_digest": prepared.ReviewContractContext.InputFilesDigest,
	}
	var amendments []string
	for _, request := range loaded.RevisionRequests {
		if request.Source != supervisorRevisionSource && request.Source != finalizeRevisionSource {
			amendments = append(amendments, request.Text)
		}
	}
	inputs["amendments"] = amendments
	if !addPhaseInputs(inputs, phase, prepared.CWD) {
		return ""
	}
	data, err := json.Marshal(inputs)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func reusableSkeletonFiles(workDir string, result *skeletonpreflight.Result) bool {
	for _, output := range result.Outputs {
		if !filepath.IsLocal(output.Path) {
			return false
		}
		path := filepath.Join(workDir, output.Path)
		real := pathutil.CleanPhysical(path)
		rel, err := filepath.Rel(pathutil.CleanPhysical(workDir), real)
		if err != nil || !filepath.IsLocal(rel) {
			return false
		}
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			return false
		}
	}
	return true
}

// repositorySignals hashes the setup stage's repository inputs. It reports
// false when any signal cannot establish freshness, which disables setup reuse.
func repositorySignals(workDir string) (map[string]string, bool) {
	signals := make(map[string]string)
	for _, path := range setuppreflight.DiscoverRepositorySignals(workDir) {
		full := filepath.Join(workDir, path)
		info, err := os.Lstat(full)
		data, err := readSetupSignal(full, info, err)
		if err != nil {
			fmt.Fprintf(os.Stderr, "galley: setup reuse disabled: %v\n", err)
			return nil, false
		}
		signals[path] = fmt.Sprintf("%s:%x", info.Mode(), sha256.Sum256(data))
	}
	return signals, true
}

// readSetupSignal reads a regular setup input; a symlink or any other file kind
// cannot establish freshness.
func readSetupSignal(full string, info os.FileInfo, statErr error) ([]byte, error) {
	if statErr != nil {
		return nil, statErr
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("symlinked setup input cannot establish freshness: %s", full)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("not a regular setup input: %s", full)
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return nil, fmt.Errorf("read setup input %s: %w", full, err)
	}
	return data, nil
}

// addPhaseInputs adds the phase-specific reuse key inputs. It reports false when
// the setup phase cannot establish repository freshness, which disables reuse.
func addPhaseInputs(inputs map[string]any, phase, workDir string) bool {
	if phase != "setup" {
		inputs["schema"] = schemas.AcceptanceSkeletonManifest
		inputs["prompts"] = []string{prompts.AcceptanceSkeletonCreatorCodex(), prompts.AcceptanceSkeletonCreator(), prompts.AcceptanceSkeletonCreatorGrok()}
		return true
	}
	signals, ok := repositorySignals(workDir)
	if !ok {
		return false
	}
	inputs["repository_signals"] = signals
	inputs["schema"] = schemas.SetupResult
	inputs["prompts"] = []string{prompts.SetupExecutorCodex(), prompts.SetupExecutorClaude(), prompts.SetupExecutorGrok()}
	return true
}
