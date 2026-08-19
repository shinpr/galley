package task

import (
	"fmt"
	"strings"

	"github.com/shinpr/galley/internal/profile"
)

func RenderWorkOrder(t Task) string {
	return RenderWorkOrderWithProfiles(t, profile.Bundle{})
}

func RenderWorkOrderWithProfiles(t Task, profiles profile.Bundle) string {
	t = Defaulted(t)
	var b strings.Builder
	pendingRevisions := pendingRevisionRequests(t.RevisionRequests)

	fmt.Fprintf(&b, "# Galley Work Order\n\n")
	fmt.Fprintf(&b, "Task ID: `%s`\n", t.ID)
	fmt.Fprintf(&b, "Mode: `%s`\n", t.Mode)
	fmt.Fprintf(&b, "Review iteration: `%d`\n\n", t.Supervisor.ReviewIterations)
	if len(pendingRevisions) > 0 {
		renderRevisionContext(&b, t, pendingRevisions)
	}
	fmt.Fprintf(&b, "## Goal\n\n%s\n\n", t.Goal)
	renderReviewContext(&b, t, len(pendingRevisions) == 0)

	fmt.Fprintf(&b, "## Acceptance Criteria\n\n")
	for _, ac := range t.AcceptanceCriteria {
		fmt.Fprintf(&b, "- `%s`: %s\n  Verification: %s\n", ac.ID, ac.Text, ac.Verification)
	}
	renderInputFiles(&b, t)

	fmt.Fprintf(&b, "\n## Scope\n\n")
	fmt.Fprintf(&b, "- cwd: `%s`\n", t.Scope.CWD)
	fmt.Fprintf(&b, "- permission: `%s`\n", t.Scope.Permission)
	fmt.Fprintf(&b, "- allowed paths: `%s`\n", strings.Join(t.Scope.AllowedPaths, "`, `"))
	if len(t.Scope.ForbiddenPaths) > 0 {
		fmt.Fprintf(&b, "- forbidden paths: `%s`\n", strings.Join(t.Scope.ForbiddenPaths, "`, `"))
	}

	fmt.Fprintf(&b, "\n## Execution Policy\n\n")
	fmt.Fprintf(&b, "- loop budget: `%v`\n", t.ExecutionPolicy.LoopBudget)
	fmt.Fprintf(&b, "- timeout ms: `%d`\n", t.ExecutionPolicy.TimeoutMS)
	if t.Mode == "afk" {
		fmt.Fprintf(&b, "- AFK decision policy: `%s`\n", DefaultAFKDecisionPolicy)
	}
	renderProfileContext(&b, profiles)
	renderPreflightObligations(&b, t)

	fmt.Fprintf(&b, "\n## Required Behavior\n\n")
	fmt.Fprintf(&b, "- Read relevant files before editing.\n")
	fmt.Fprintf(&b, "- Treat allowed paths as the expected implementation scope. Make an outside-allowed edit only when the extracted work contract or a pending revision request requires it; keep it minimal and record it as a scope expansion.\n")
	fmt.Fprintf(&b, "- Treat forbidden paths as protected.\n")
	fmt.Fprintf(&b, "- If requirements are ambiguous, choose the smallest reversible implementation, record the decision, and continue.\n")
	fmt.Fprintf(&b, "- Run the highest-value verification available in this environment.\n")
	fmt.Fprintf(&b, "- Return exactly one JSON object matching the configured schema.\n")

	return b.String()
}

func pendingRevisionRequests(requests []RevisionRequest) []RevisionRequest {
	pending := make([]RevisionRequest, 0, len(requests))
	for _, request := range requests {
		if request.Status != "addressed" {
			pending = append(pending, request)
		}
	}
	return pending
}

func renderRevisionContext(b *strings.Builder, t Task, requests []RevisionRequest) {
	fmt.Fprintf(b, "## Revision Objective\n\n")
	fmt.Fprintf(b, "Resolve every pending revision request as a coherent repair batch. Apply non-supervisor requests in displayed order as amendments only to the affected acceptance criterion or verification guidance; a later request supersedes an earlier request only where they conflict. Preserve every unaffected task term, gate, and verified pass. This attempt is complete only when every pending request has concrete implementation and verification evidence.\n\n")

	fmt.Fprintf(b, "## Findings To Address This Attempt\n\n")
	for _, risk := range t.Risks {
		if strings.HasPrefix(risk.ID, "requeue-") {
			fmt.Fprintf(b, "- supervisor work order: %s\n", risk.Detail)
		}
	}
	for _, request := range requests {
		fmt.Fprintf(b, "- `%s` source=`%s`", request.ID, request.Source)
		if request.CommentID != "" {
			fmt.Fprintf(b, " comment=`%s`", request.CommentID)
		}
		fmt.Fprintf(b, ": %s\n", request.Text)
		fmt.Fprintf(b, "  - executor result ID: `revision:%s`\n", request.ID)
	}
	fmt.Fprintf(b, "\nReturn each request as one `acceptance_criteria` entry with the displayed `revision:<id>`, status, and concrete evidence. When requests describe the same underlying defect, use one consistent repair while returning evidence for each request ID.\n\n")

	if t.ReviewProgress != nil && (len(t.ReviewProgress.Acceptance) > 0 || len(t.ReviewProgress.Quality) > 0) {
		fmt.Fprintf(b, "## Verified Passes To Preserve\n\n")
		fmt.Fprintf(b, "The supervisor has already verified these items. Preserve them and recheck any item this attempt can affect.\n\n")
		if len(t.ReviewProgress.Acceptance) > 0 {
			fmt.Fprintf(b, "- acceptance: `%s`\n", strings.Join(t.ReviewProgress.Acceptance, "`, `"))
		}
		if len(t.ReviewProgress.Quality) > 0 {
			fmt.Fprintf(b, "- quality: `%s`\n", strings.Join(t.ReviewProgress.Quality, "`, `"))
		}
		fmt.Fprintf(b, "\n")
	}
}
func renderInputFiles(b *strings.Builder, t Task) {
	if len(t.Files) == 0 {
		return
	}
	fmt.Fprintf(b, "\n## Input Files\n\n")
	fmt.Fprintf(b, "Galley has placed these files in the execution workspace before this attempt. Use them as task context and preserve or modify them according to the commit policy.\n\n")
	for _, file := range t.Files {
		fmt.Fprintf(b, "- `%s`", file.Destination)
		if file.Description != "" {
			fmt.Fprintf(b, ": %s", file.Description)
		}
		fmt.Fprintf(b, "\n  - source: `%s`\n", file.Source)
		fmt.Fprintf(b, "  - commit with task changes: `%t`\n", file.Commit)
		if !file.Commit {
			fmt.Fprintf(b, "  - cleanup: Galley removes this file before commit/PR finalization.\n")
		}
	}
}

func renderReviewContext(b *strings.Builder, t Task, includeRequeueInstructions bool) {
	var requeueInstructions []Risk
	var otherRisks []Risk
	for _, risk := range t.Risks {
		if strings.HasPrefix(risk.ID, "requeue-") {
			requeueInstructions = append(requeueInstructions, risk)
			continue
		}
		otherRisks = append(otherRisks, risk)
	}
	if t.PR.URL != "" || (includeRequeueInstructions && len(requeueInstructions) > 0) {
		fmt.Fprintf(b, "## PR Review Context\n\n")
		if t.PR.URL != "" {
			fmt.Fprintf(b, "- PR: `%s`\n", t.PR.URL)
		}
		if t.Supervisor.ReviewIterations > 0 {
			fmt.Fprintf(b, "- review iteration: `%d`\n", t.Supervisor.ReviewIterations)
		}
		if includeRequeueInstructions {
			for _, instruction := range requeueInstructions {
				fmt.Fprintf(b, "- additional instruction `%s`: %s\n", instruction.ID, instruction.Detail)
			}
		}
		fmt.Fprintf(b, "\n")
	}
	if len(otherRisks) > 0 {
		fmt.Fprintf(b, "## Existing Risks\n\n")
		for _, risk := range otherRisks {
			fmt.Fprintf(b, "- `%s` %s: %s\n  Mitigation: %s\n", risk.ID, risk.Type, risk.Detail, risk.Mitigation)
		}
		fmt.Fprintf(b, "\n")
	}
	if len(t.Decisions) > 0 {
		fmt.Fprintf(b, "## Prior Decisions\n\n")
		for _, decision := range t.Decisions {
			fmt.Fprintf(b, "- `%s` %s -> %s\n  Rationale: %s\n", decision.ID, decision.Question, decision.Chosen, decision.Rationale)
		}
		fmt.Fprintf(b, "\n")
	}
}

// renderPreflightObligations renders concrete skeleton paths, AC bindings,
// kinds, and purposes derived from daemon-owned task YAML declarations. The
// daemon may further augment this with the runtime preflight result; this
// static rendering is what `galley task work-order` prints offline.
func renderPreflightObligations(b *strings.Builder, t Task) {
	if t.Preflight == nil || t.Preflight.AcceptanceSkeleton == nil || !t.Preflight.AcceptanceSkeleton.IsEnabled() {
		return
	}
	fmt.Fprintf(b, "\n## Acceptance Skeleton Obligations\n\n")
	cfg := t.Preflight.AcceptanceSkeleton
	if len(cfg.Outputs) == 0 {
		fmt.Fprintf(b, "Galley will run the built-in test creator before this attempt. Read each generated skeleton, complete the implementation it verifies, and keep the assertions meaningful. Do not delete skeleton files, leave placeholder assertions, skip the tests, or weaken their assertions.\n")
		return
	}
	fmt.Fprintf(b, "Galley pre-created the following AC-linked test skeletons in the worktree before this attempt. Read each skeleton, complete the implementation it verifies, and keep the assertions meaningful.\n\n")
	for _, out := range cfg.Outputs {
		fmt.Fprintf(b, "- AC `%s` -> `%s` (kind=%s, implementation_required=%t)\n", out.ACID, out.Path, out.Kind, out.ImplementationRequired)
		fmt.Fprintf(b, "  Purpose: %s\n", out.Purpose)
		if out.Satisfies != "" {
			fmt.Fprintf(b, "  Satisfies: %s\n", out.Satisfies)
		}
		if out.IntegrationPoint != "" {
			fmt.Fprintf(b, "  Integration point: %s\n", out.IntegrationPoint)
		}
	}
	fmt.Fprintf(b, "\nCompletion obligations: every implementation_required skeleton above must be implemented and covered by the normal required verification checks before the supervisor accepts the attempt.\n")
}

func renderProfileContext(b *strings.Builder, profiles profile.Bundle) {
	if profiles.Quality != nil {
		fmt.Fprintf(b, "\n## Quality Profile\n\n")
		fmt.Fprintf(b, "- id: `%s`\n", profiles.Quality.ID)
		fmt.Fprintf(b, "- min score: `%d`\n", profiles.Quality.PassPolicy.MinScore)
		for _, check := range profiles.Quality.RequiredChecks {
			fmt.Fprintf(b, "- check `%s` required=%t commands=`%s`\n", check.ID, check.Required, strings.Join(check.PreferredCommands, "`, `"))
		}
		for _, dimension := range profiles.Quality.ReviewDimensions {
			fmt.Fprintf(b, "- dimension `%s` weight=%d required=%t pass=%s\n", dimension.ID, dimension.Weight, dimension.Required, dimension.Pass)
		}
	}
	if profiles.Environment != nil {
		fmt.Fprintf(b, "\n## Environment Profile\n\n")
		fmt.Fprintf(b, "- id: `%s`\n", profiles.Environment.ID)
		fmt.Fprintf(b, "- cwd: `%s`\n", profiles.Environment.CWD)
		for name, command := range profiles.Environment.Commands {
			fmt.Fprintf(b, "- command `%s`: `%s`\n", name, command)
		}
		if profiles.Environment.Executor != nil && profiles.Environment.Executor.DefaultCLI != "" {
			fmt.Fprintf(b, "- executor default: `%s`\n", profiles.Environment.Executor.DefaultCLI)
		}
		fmt.Fprintf(b, "- network: `%s`\n", profiles.Environment.Constraints.Network)
		fmt.Fprintf(b, "- secrets policy: `%s`\n", profiles.Environment.Constraints.SecretsPolicy)
		fmt.Fprintf(b, "- destructive commands: `%s`\n", profiles.Environment.Constraints.DestructiveCommands)
	}
}
