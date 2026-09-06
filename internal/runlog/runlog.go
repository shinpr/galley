package runlog

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
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
		return "", fmt.Errorf("read runs dir %s: %w", runsDir, err)
	}
	best := ""
	var bestN uint64
	prefix := taskID + "-"
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		n, err := strconv.ParseUint(strings.TrimPrefix(e.Name(), prefix), 10, 63)
		if err != nil {
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
		return "", -1, fmt.Errorf("read run dir %s: %w", runDir, err)
	}
	best := ""
	bestN := -1
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "attempt-") {
			continue
		}
		n, err := strconv.Atoi(strings.TrimPrefix(e.Name(), "attempt-"))
		if err != nil {
			continue
		}
		if n > bestN {
			best = filepath.Join(runDir, e.Name())
			bestN = n
		}
	}
	return best, bestN, nil
}
