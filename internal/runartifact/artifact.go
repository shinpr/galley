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
	SetupCodexDirname              = "setup-codex"
	SetupResultFilename            = "setup_result.json"
	SetupEnvironmentUpdateFilename = "environment_update.json"
	SetupExecutorPlanFilename      = "setup_executor_command_plan.json"
	SetupExecutorStdoutFilename    = "setup_executor.stdout.jsonl"
	SetupExecutorStderrFilename    = "setup_executor.stderr.log"
	GrokSetupCompletionFilename    = "grok_setup_completion.json"

	// Acceptance skeleton preflight artifacts.
	PreflightResultFilename          = "preflight_result.json"
	PreflightCreatorPlanFilename     = "preflight_creator_command_plan.json"
	PreflightCreatorManifestFilename = "preflight_creator_manifest.json"
	GrokSkeletonCompletionFilename   = "grok_acceptance_skeleton_completion.json"

	// Executor and supervisor attempt artifacts.
	ExecutorResultFilename         = "executor_result.json"
	RunResultFilename              = "run_result.json"
	CommandPlanFilename            = "command_plan.json"
	GrokCompletionMetadataFilename = "grok_completion.json"
	// ExecutorTerminalFilename records the normal-terminal versus interruption
	// routing decision Galley derived from runner state and provider output.
	ExecutorTerminalFilename       = "executor_terminal.json"
	SupervisorVerdictFilename      = "supervisor_verdict.json"
	ModelSupervisorVerdictFilename = "model_supervisor_verdict.json"
	SupervisorErrorFilename        = "supervisor_error.json"
	SupervisorTryDirname           = "supervisor-try-1"
	SupervisorEvidenceFilename     = "supervisor.json"
	PRBodyFilename                 = "pr_body.md"

	// VCS review artifacts.
	GitStatusFilename = "git_status.json"
	DiffPatchFilename = "diff.patch"
)

// AttemptDirname returns the stable run-evidence directory for one attempt.
func AttemptDirname(number int) string {
	return fmt.Sprintf("attempt-%d", number)
}

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
