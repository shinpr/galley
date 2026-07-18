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
	Status             string               `json:"status"`
	Summary            string               `json:"summary"`
	AcceptanceGaps     []string             `json:"acceptance_gaps"`
	ReviewedFiles      []string             `json:"reviewed_files"`
	AcceptanceEvidence []AcceptanceEvidence `json:"acceptance_evidence"`
	QualityPasses      []string             `json:"quality_passes"`
	QualityGaps        []string             `json:"quality_gaps"`
	Findings           []Finding            `json:"findings"`
	ResidualRisks      []string             `json:"residual_risks"`
	DiscussionItems    []DiscussionItem     `json:"discussion_items"`
	Confidence         string               `json:"confidence"`
	NextWorkOrder      string               `json:"next_work_order"`
}

// AcceptanceEvidence links one task or revision acceptance item to concrete evidence.
type AcceptanceEvidence struct {
	ACID     string   `json:"ac_id"`
	Evidence []string `json:"evidence"`
}

// Finding is a structured supervisor review issue.
type Finding struct {
	Severity         string `json:"severity"`
	Category         string `json:"category"`
	File             string `json:"file"`
	Summary          string `json:"summary"`
	BlocksAcceptance bool   `json:"blocks_acceptance"`
}

// DiscussionItem records accepted-work follow-up context for human reviewers.
type DiscussionItem struct {
	Topic                 string `json:"topic"`
	Summary               string `json:"summary"`
	RequiresHumanDecision bool   `json:"requires_human_decision"`
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
