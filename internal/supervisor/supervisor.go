package supervisor

import (
	"github.com/shinpr/galley/internal/profile"
	"github.com/shinpr/galley/internal/runner"
	"github.com/shinpr/galley/internal/task"
)

// Verdict is the supervisor decision for one executor attempt.
type Verdict struct {
	Status             string               `json:"status"`
	Summary            string               `json:"summary"`
	AcceptanceGaps     []string             `json:"acceptance_gaps,omitempty"`
	ReviewedFiles      []string             `json:"reviewed_files,omitempty"`
	AcceptanceEvidence []AcceptanceEvidence `json:"acceptance_evidence,omitempty"`
	QualityCoverage    []QualityCoverage    `json:"quality_coverage"`
	Findings           []Finding            `json:"findings,omitempty"`
	ResidualRisks      []string             `json:"residual_risks,omitempty"`
	DiscussionItems    []DiscussionItem     `json:"discussion_items,omitempty"`
	Confidence         string               `json:"confidence,omitempty"`
	NextWorkOrder      string               `json:"next_work_order,omitempty"`
}

type QualityCoverage struct {
	Criterion       string   `json:"criterion"`
	ChangedSurface  string   `json:"changed_surface"`
	EvidenceChecked []string `json:"evidence_checked"`
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
	File             string `json:"file,omitempty"`
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
	Task            task.Task
	Profiles        profile.Bundle
	ExecutorResult  runner.ExecutorResult
	ParseError      error
	RunError        error
	DiffDirty       bool
	Diff            string
	DiffError       error
	Attempt         int
	AttemptsLeft    int
	PreflightResult any
	// SetupResult carries the setup executor preflight outcome (authored or
	// learned) so reviewers see the readiness facts that gated the executor.
	SetupResult any
	// SetupEnvironmentUpdate carries the optional learned-plan profile update
	// record, present when Galley rewrote environment.yaml with a learned setup.
	SetupEnvironmentUpdate any
}
