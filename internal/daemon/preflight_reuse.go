package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	setuppreflight "github.com/shinpr/galley/internal/preflight/setup"
	skeletonpreflight "github.com/shinpr/galley/internal/preflight/skeleton"
	"github.com/shinpr/galley/internal/runartifact"
	"github.com/shinpr/galley/internal/task"
	"github.com/shinpr/galley/internal/vcs"
	"github.com/shinpr/galley/internal/workspace"
)

func reuseReadySetup(root, taskID, runDir string, effective task.Executor, inputKey string) (*setuppreflight.Result, bool, error) {
	dirs, err := priorTaskRunDirs(root, taskID, runDir)
	if err != nil {
		return nil, false, err
	}
	for _, dir := range dirs {
		if !preflightInputsMatch(dir, "setup", inputKey) {
			continue
		}
		res, err := setuppreflight.LoadResult(dir)
		if err != nil {
			continue
		}
		if res == nil || res.Status != setuppreflight.StatusReady {
			continue
		}
		if !res.MatchesExecutor(effective) {
			continue
		}
		if err := setuppreflight.WriteResult(runDir, res); err != nil {
			return nil, false, err
		}
		if err := recordPreflightInputs(runDir, "setup", inputKey, dir); err != nil {
			return nil, false, err
		}
		return res, true, nil
	}
	return nil, false, nil
}

func reuseCompletedAcceptanceSkeleton(root, taskID, runDir string, effective task.Executor, inputKey, workDir string) (*skeletonpreflight.Result, bool, error) {
	dirs, err := priorTaskRunDirs(root, taskID, runDir)
	if err != nil {
		return nil, false, err
	}
	for _, dir := range dirs {
		if !preflightInputsMatch(dir, "skeleton", inputKey) {
			continue
		}
		res, err := skeletonpreflight.LoadResult(dir)
		if err != nil {
			continue
		}
		if res == nil || res.Status != "completed" {
			continue
		}
		if !res.MatchesExecutor(effective) {
			continue
		}
		if !reusableSkeletonFiles(workDir, res) {
			continue
		}
		if err := skeletonpreflight.WriteResult(runDir, res); err != nil {
			return nil, false, err
		}
		if err := recordPreflightInputs(runDir, "skeleton", inputKey, dir); err != nil {
			return nil, false, err
		}
		return res, true, nil
	}
	return nil, false, nil
}

func priorTaskRunDirs(root, taskID, currentRunDir string) ([]string, error) {
	runsDir := filepath.Join(root, "runs")
	entries, err := os.ReadDir(runsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	prefix := taskID + "-"
	type candidate struct {
		path string
		n    uint64
	}
	var candidates []candidate
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		n, err := strconv.ParseUint(strings.TrimPrefix(entry.Name(), prefix), 10, 63)
		if err != nil {
			continue
		}
		path := filepath.Join(runsDir, entry.Name())
		if filepath.Clean(path) == filepath.Clean(currentRunDir) {
			continue
		}
		candidates = append(candidates, candidate{path: path, n: n})
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].n > candidates[j].n })
	dirs := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		dirs = append(dirs, candidate.path)
	}
	return dirs, nil
}

func preflightReuseError(phase string, err error) error {
	return fmt.Errorf("reuse completed %s evidence: %w", phase, err)
}

// retainPriorReviewBase keeps a prior run's review base while a finalization
// failure is still pending, so its accepted commit stays inside the diff.
func retainPriorReviewBase(ctx context.Context, opts Options, loaded task.Task, runDir string, prepared *workspace.Prepared) {
	if prepared == nil || !prepared.WorktreeReused || prepared.BaseSHA == "" {
		return
	}
	if !hasPendingFinalizeRevision(loaded) {
		return
	}
	taskID := loaded.ID
	prior, err := priorRunBaseSHA(opts.Root, taskID, runDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "galley: task %s could not read the prior review base: %v\n", taskID, err)
		return
	}
	if prior == "" || prior == prepared.BaseSHA {
		return
	}
	ancestor, err := vcs.IsAncestor(ctx, vcsBinaries(opts), prepared.CWD, prior, prepared.BaseSHA)
	if err != nil {
		fmt.Fprintf(os.Stderr, "galley: task %s could not verify the prior review base %s: %v\n", taskID, prior, err)
		return
	}
	if !ancestor {
		return
	}
	prepared.BaseSHA = prior
}

// priorRunBaseSHA returns the review base recorded by the most recent prior
// run of this task, or "" when no prior run recorded one.
func priorRunBaseSHA(root, taskID, currentRunDir string) (string, error) {
	dirs, err := priorTaskRunDirs(root, taskID, currentRunDir)
	if err != nil {
		return "", err
	}
	for _, dir := range dirs {
		data, err := os.ReadFile(runartifact.Path(dir, runartifact.WorkspaceFilename))
		if err != nil {
			continue
		}
		var prior workspace.Prepared
		if err := json.Unmarshal(data, &prior); err != nil {
			continue
		}
		if prior.BaseSHA != "" {
			return prior.BaseSHA, nil
		}
	}
	return "", nil
}
