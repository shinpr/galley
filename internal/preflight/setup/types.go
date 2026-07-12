package setup

import (
	"context"

	"github.com/shinpr/galley/internal/profile"
	"github.com/shinpr/galley/internal/task"
)

// ResultStatus enumerates the setup phase outcomes Galley records.
const (
	StatusReady  = "ready"
	StatusFailed = "failed"
)

// Setup-phase classification constants used by daemon failure routing.
const (
	Phase      = "setup"
	FailedKind = "setup_failed"
)

// Source values recorded in CommandAttempt.Source so reviewers can
// distinguish prior-plan commands from learned commands.
const (
	SourceEnvironmentSetup    = "environment_setup"
	SourceEnvironmentCommands = "environment_commands"
	SourceDiscovered          = "discovered"
	SourceReadinessCheck      = "readiness_check"
)

const (
	maxResultCommands      = 50
	maxResultFiles         = 100
	maxResultExcerptLength = 400
	maxResultTextLength    = 2048
)

// Result is the runtime source-of-truth output of the setup executor
// preflight. It is serialized to runs/<run-id>/setup_result.json.
type Result struct {
	Status             string                 `json:"status" yaml:"status"`
	Commands           []CommandAttempt       `json:"commands" yaml:"commands"`
	SuccessfulCommands []profile.SetupCommand `json:"successful_commands,omitempty" yaml:"successful_commands,omitempty"`
	InspectedFiles     []string               `json:"inspected_files,omitempty" yaml:"inspected_files,omitempty"`
	ReadinessEvidence  string                 `json:"readiness_evidence,omitempty" yaml:"readiness_evidence,omitempty"`
	RepairGuidance     string                 `json:"repair_guidance,omitempty" yaml:"repair_guidance,omitempty"`
	Error              string                 `json:"error,omitempty" yaml:"error,omitempty"`
	Provider           string                 `json:"provider,omitempty" yaml:"provider,omitempty"`
	Source             string                 `json:"source,omitempty" yaml:"source,omitempty"`
}

// CommandAttempt is one command the setup executor attempted. Stdout/stderr
// are truncated excerpts; the full subprocess output remains in the setup
// executor logs under the run directory.
type CommandAttempt struct {
	Run           string `json:"run" yaml:"run"`
	Why           string `json:"why,omitempty" yaml:"why,omitempty"`
	Source        string `json:"source" yaml:"source"`
	ExitCode      int    `json:"exit_code" yaml:"exit_code"`
	StdoutExcerpt string `json:"stdout_excerpt,omitempty" yaml:"stdout_excerpt,omitempty"`
	StderrExcerpt string `json:"stderr_excerpt,omitempty" yaml:"stderr_excerpt,omitempty"`
}

// EnvironmentUpdate is persisted to runs/<run-id>/environment_update.json
// when the daemon rewrites the repository environment.yaml setup field.
type EnvironmentUpdate struct {
	ProfilePath string             `json:"profile_path"`
	Changed     bool               `json:"changed"`
	Before      *profile.SetupPlan `json:"before,omitempty"`
	After       profile.SetupPlan  `json:"after"`
	Diff        string             `json:"diff,omitempty"`
	Reason      string             `json:"reason"`
	UpdatedAt   string             `json:"updated_at"`
}

// Options configures one preflight invocation. The
// EnvironmentProfilePath is required when the daemon should persist a learned
// setup plan back to the repository environment profile.
type Options struct {
	Task                   task.Task
	WorkDir                string
	RunDir                 string
	Profiles               profile.Bundle
	ClaudeBin              string
	CodexBin               string
	GrokBin                string
	GLMAuthToken           string
	EnvironmentProfilePath string
	// RepositorySignals is an optional list of repository setup signal paths
	// (manifests, lockfiles, setup docs) that Galley surfaces to the setup
	// executor. The daemon discovers these from the worktree before invoking the
	// executor so the prompt context is grounded in real repository evidence.
	RepositorySignals []string
	ExecutorRunner    func(context.Context, Options) (*Result, error)
}
