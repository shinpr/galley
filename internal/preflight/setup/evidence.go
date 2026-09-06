package setup

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/shinpr/galley/internal/jsonio"
	"github.com/shinpr/galley/internal/runartifact"
)

func recordSetupProfileUpdateFailure(runDir string, err error) error {
	payload := map[string]any{
		"changed": false,
		"error":   err.Error(),
	}
	if writeErr := jsonio.Write(runartifact.Path(runDir, runartifact.SetupEnvironmentUpdateFilename), payload); writeErr != nil {
		return fmt.Errorf("write setup environment update failure evidence: %w", writeErr)
	}
	return nil
}

// WriteResult persists the source-of-truth setup_result.json.
func WriteResult(runDir string, res *Result) error {
	if runDir == "" {
		return fmt.Errorf("run dir is required for setup result")
	}
	if res == nil {
		return nil
	}
	normalizeResult(res)
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		return fmt.Errorf("create setup run dir: %w", err)
	}
	return jsonio.Write(runartifact.Path(runDir, runartifact.SetupResultFilename), res)
}

// LoadEnvironmentUpdate reads the persisted environment_update.json.
// Returns (nil, nil) when the file does not exist so callers can probe
// unconditionally.
func LoadEnvironmentUpdate(runDir string) (*EnvironmentUpdate, error) {
	path := runartifact.Path(runDir, runartifact.SetupEnvironmentUpdateFilename)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read environment update: %w", err)
	}
	var update EnvironmentUpdate
	if err := json.Unmarshal(data, &update); err != nil {
		return nil, fmt.Errorf("decode environment update: %w", err)
	}
	return &update, nil
}

// LoadRunEvidence reads the persisted setup_result.json and
// environment_update.json from runDir. Evidence read failures are returned as a
// degraded setup result so later prompts still see that setup evidence was
// unavailable instead of silently losing the setup context.
func LoadRunEvidence(runDir, runID string) (*Result, *EnvironmentUpdate) {
	res, err := LoadResult(runDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "galley: could not load setup result for run %s: %v\n", runID, err)
		res = &Result{
			Status:         StatusFailed,
			Commands:       []CommandAttempt{},
			Error:          "setup_result.json could not be loaded: " + err.Error(),
			RepairGuidance: "Inspect the run directory and restore readable setup_result.json evidence before trusting setup readiness.",
		}
	}
	update, uerr := LoadEnvironmentUpdate(runDir)
	if uerr != nil {
		fmt.Fprintf(os.Stderr, "galley: could not load setup environment update for run %s: %v\n", runID, uerr)
		res = recordEnvironmentUpdateFailure(res, uerr)
	}
	return res, update
}

// LoadResult reads the persisted setup_result.json. Returns (nil, nil)
// when the file does not exist so callers can probe unconditionally.
func LoadResult(runDir string) (*Result, error) {
	path := runartifact.Path(runDir, runartifact.SetupResultFilename)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read setup result: %w", err)
	}
	var res Result
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, fmt.Errorf("decode setup result: %w", err)
	}
	return &res, nil
}

// WriteEnvironmentUpdate persists the profile-rewrite audit record.
func WriteEnvironmentUpdate(runDir string, update *EnvironmentUpdate) error {
	if update == nil {
		return nil
	}
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		return fmt.Errorf("create setup run dir: %w", err)
	}
	return jsonio.Write(runartifact.Path(runDir, runartifact.SetupEnvironmentUpdateFilename), update)
}

// recordEnvironmentUpdateFailure folds an unreadable environment_update.json
// into the setup result so readers cannot mistake it for a healthy setup.
func recordEnvironmentUpdateFailure(res *Result, uerr error) *Result {
	if res == nil {
		res = &Result{Status: StatusFailed, Commands: []CommandAttempt{}}
	}
	detail := "environment_update.json could not be loaded: " + uerr.Error()
	if res.Error == "" {
		res.Error = detail
	} else {
		res.Error += "; " + detail
	}
	if res.RepairGuidance == "" {
		res.RepairGuidance = "Inspect the run directory and restore readable setup evidence before trusting setup readiness."
	}
	return res
}
