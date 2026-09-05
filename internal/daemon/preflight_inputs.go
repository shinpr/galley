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
func preflightInputKey(phase string, loaded task.Task, profiles profile.Bundle, prepared claimedWorkspace, executor task.Executor) string {
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
	if phase == "setup" {
		signals := make(map[string]string)
		for _, path := range setuppreflight.DiscoverRepositorySignals(prepared.CWD) {
			full := filepath.Join(prepared.CWD, path)
			info, err := os.Lstat(full)
			var data []byte
			if err == nil && info.Mode()&os.ModeSymlink != 0 {
				err = fmt.Errorf("symlinked setup input cannot establish freshness: %s", full)
			} else if err == nil && info.Mode().IsRegular() {
				data, err = os.ReadFile(full)
			} else if err == nil {
				err = fmt.Errorf("not a regular setup input: %s", full)
			}
			if err != nil {
				fmt.Fprintf(os.Stderr, "galley: setup reuse disabled: %v\n", err)
				return ""
			}
			signals[path] = fmt.Sprintf("%s:%x", info.Mode(), sha256.Sum256(data))
		}
		inputs["repository_signals"] = signals
		inputs["schema"] = schemas.SetupResult
		inputs["prompts"] = []string{prompts.SetupExecutorCodex(), prompts.SetupExecutorClaude(), prompts.SetupExecutorGrok()}
	} else {
		inputs["schema"] = schemas.AcceptanceSkeletonManifest
		inputs["prompts"] = []string{prompts.AcceptanceSkeletonCreatorCodex(), prompts.AcceptanceSkeletonCreator(), prompts.AcceptanceSkeletonCreatorGrok()}
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
