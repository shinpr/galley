package runlog

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// LatestTaskRunDir returns the newest <task-id>-<timestamp> run directory
// under root/runs. It returns an empty path when no matching run exists.
func LatestTaskRunDir(root, taskID string) (string, error) {
	runsDir := filepath.Join(root, "runs")
	entries, err := os.ReadDir(runsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	best := ""
	var bestN int64
	prefix := taskID + "-"
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		var n int64
		if _, err := fmt.Sscanf(strings.TrimPrefix(e.Name(), prefix), "%d", &n); err != nil {
			continue
		}
		if best == "" || n > bestN {
			best = filepath.Join(runsDir, e.Name())
			bestN = n
		}
	}
	return best, nil
}

// LatestAttemptDir returns the highest-numbered attempt-N directory under
// runDir. It returns an empty path and -1 when no attempt directory exists.
func LatestAttemptDir(runDir string) (string, int, error) {
	entries, err := os.ReadDir(runDir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", -1, nil
		}
		return "", -1, err
	}
	best := ""
	bestN := -1
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "attempt-") {
			continue
		}
		var n int
		if _, err := fmt.Sscanf(e.Name(), "attempt-%d", &n); err != nil {
			continue
		}
		if n > bestN {
			best = filepath.Join(runDir, e.Name())
			bestN = n
		}
	}
	return best, bestN, nil
}
