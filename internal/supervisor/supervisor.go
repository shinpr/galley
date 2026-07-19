package supervisor

import (
	"errors"

	"github.com/shinpr/galley/internal/profile"
	"github.com/shinpr/galley/internal/runner"
	"github.com/shinpr/galley/internal/task"
)

// VerdictContractError reports model output that cannot satisfy Galley's
// supervisor verdict contract.
type VerdictContractError struct {
	Err error
}

func (e *VerdictContractError) Error() string {
	return e.Err.Error()
}

func (e *VerdictContractError) Unwrap() error {
	return e.Err
}

func IsVerdictContractError(err error) bool {
	var contractErr *VerdictContractError
	return errors.As(err, &contractErr)
}

// Verdict is the supervisor decision for one executor attempt.
type Verdict struct {
	Status           string   `json:"status"`
	Summary          string   `json:"summary"`
	AcceptancePasses []string `json:"acceptance_passes"`
	QualityPasses    []string `json:"quality_passes"`
	Findings         []string `json:"findings"`
	DiscussionItems  []string `json:"discussion_items"`
}

// Evidence is the local evidence sent to a model supervisor.
type Evidence struct {
	Task                  task.Task
	Profiles              profile.Bundle
	ReviewContractContext ReviewContractContext
	ExecutorResult        runner.ExecutorResult
	ParseError            error
	RunError              error
	DiffDirty             bool
	Diff                  string
	DiffError             error
	Attempt               int
	AttemptsLeft          int
	PreflightResult       any
	// SetupResult carries the setup executor preflight outcome (authored or
	// learned) so reviewers see the readiness facts that gated the executor.
	SetupResult any
	// SetupEnvironmentUpdate carries the optional learned-plan profile update
	// record, present when Galley rewrote environment.yaml with a learned setup.
	SetupEnvironmentUpdate any
}
