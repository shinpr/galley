package runartifact

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/shinpr/galley/internal/jsonio"
)

const (
	// Task and daemon stage snapshots.
	TaskSnapshotFilename          = "task.yaml"
	EffectiveTaskSnapshotFilename = "task.effective.yaml"
	ValidationFilename            = "validation.json"
	ProfilesFilename              = "profiles.json"
	WorkspaceFilename             = "workspace.json"
	InputFilesFilename            = "input_files.json"

	// Setup preflight artifacts.
	SetupResultFilename            = "setup_result.json"
	SetupEnvironmentUpdateFilename = "environment_update.json"
	SetupExecutorPlanFilename      = "setup_executor_command_plan.json"
	SetupExecutorStdoutFilename    = "setup_executor.stdout.jsonl"
	SetupExecutorStderrFilename    = "setup_executor.stderr.log"

	// Acceptance skeleton preflight artifacts.
	PreflightResultFilename          = "preflight_result.json"
	PreflightCreatorPlanFilename     = "preflight_creator_command_plan.json"
	PreflightCreatorManifestFilename = "preflight_creator_manifest.json"

	// Executor and supervisor attempt artifacts.
	ExecutorResultFilename         = "executor_result.json"
	RunResultFilename              = "run_result.json"
	CommandPlanFilename            = "command_plan.json"
	SupervisorVerdictFilename      = "supervisor_verdict.json"
	ModelSupervisorVerdictFilename = "model_supervisor_verdict.json"
	SupervisorErrorFilename        = "supervisor_error.json"

	// VCS review artifacts.
	GitStatusFilename = "git_status.json"
	DiffPatchFilename = "diff.patch"
)

func Path(dir, name string) string {
	return filepath.Join(dir, name)
}

func Write(dir, name string, value any) error {
	if dir == "" {
		return fmt.Errorf("artifact dir is required for %s", name)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create artifact dir: %w", err)
	}
	return jsonio.Write(Path(dir, name), value)
}

func Read[T any](dir, name string) (*T, error) {
	path := Path(dir, name)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
	var value T
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, fmt.Errorf("decode %s: %w", name, err)
	}
	return &value, nil
}
