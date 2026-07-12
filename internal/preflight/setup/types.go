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
	// ExecutorCLI/Model/Effort record the resolved executor identity used for
	// this preflight so later requeues can reuse ready evidence only when the
	// current resolved identity still matches. These fields omit omitempty so
	// an empty executor_model (provider CLI default) is persisted distinctly
	// from an absent key. Provider remains the historical CLI mirror for older
	// consumers.
	ExecutorCLI    string `json:"executor_cli" yaml:"executor_cli"`
	ExecutorModel  string `json:"executor_model" yaml:"executor_model"`
	ExecutorEffort string `json:"executor_effort" yaml:"executor_effort"`
	Source         string `json:"source,omitempty" yaml:"source,omitempty"`
}

// ApplyExecutorIdentity stamps the resolved executor identity onto the result
// before persistence. Provider is kept as a CLI mirror for evidence consumers
// that still read that field.
func ApplyExecutorIdentity(res *Result, exec task.Executor) {
	if res == nil {
		return
	}
	res.ExecutorCLI = exec.CLI
	res.ExecutorModel = exec.Model
	res.ExecutorEffort = exec.Effort
	if exec.CLI != "" {
		res.Provider = exec.CLI
	}
}

// ResolvedExecutor returns the executor identity recorded on this result.
// When executor_cli is absent, Provider is used as a legacy CLI derivation.
func (r *Result) ResolvedExecutor() task.Executor {
	if r == nil {
		return task.Executor{}
	}
	cli := r.ExecutorCLI
	if cli == "" {
		cli = r.Provider
	}
	return task.Executor{CLI: cli, Model: r.ExecutorModel, Effort: r.ExecutorEffort}
}

// MatchesExecutor reports whether this result was produced under the given
// resolved identity. Results with no known CLI never match so reuse falls
// through to a fresh preflight.
func (r *Result) MatchesExecutor(exec task.Executor) bool {
	got := r.ResolvedExecutor()
	if got.CLI == "" {
		return false
	}
	return got.CLI == exec.CLI && got.Model == exec.Model && got.Effort == exec.Effort
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
