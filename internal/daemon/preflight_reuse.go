package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	setuppreflight "github.com/shinpr/galley/internal/preflight/setup"
	skeletonpreflight "github.com/shinpr/galley/internal/preflight/skeleton"
	"github.com/shinpr/galley/internal/task"
)

func reuseReadySetup(root, taskID, runDir string, effective task.Executor) (*setuppreflight.Result, bool, error) {
	dirs, err := priorTaskRunDirs(root, taskID, runDir)
	if err != nil {
		return nil, false, err
	}
	for _, dir := range dirs {
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
		return res, true, nil
	}
	return nil, false, nil
}

func reuseCompletedAcceptanceSkeleton(root, taskID, runDir string, effective task.Executor) (*skeletonpreflight.Result, bool, error) {
	dirs, err := priorTaskRunDirs(root, taskID, runDir)
	if err != nil {
		return nil, false, err
	}
	for _, dir := range dirs {
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
		if err := skeletonpreflight.WriteResult(runDir, res); err != nil {
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
		n    int64
	}
	var candidates []candidate
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		n, err := strconv.ParseInt(strings.TrimPrefix(entry.Name(), prefix), 10, 64)
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
