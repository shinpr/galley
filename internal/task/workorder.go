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

	fmt.Fprintf(&b, "# Galley Work Order\n\n")
	fmt.Fprintf(&b, "Task ID: `%s`\n", t.ID)
	fmt.Fprintf(&b, "Mode: `%s`\n", t.Mode)
	fmt.Fprintf(&b, "Review iteration: `%d`\n\n", t.Supervisor.ReviewIterations)
	fmt.Fprintf(&b, "## Goal\n\n%s\n\n", t.Goal)
	renderReviewContext(&b, t)

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
		fmt.Fprintf(&b, "- AFK decision policy: `%s`\n", t.ExecutionPolicy.AFKDecisionPolicy)
	}
	renderProfileContext(&b, profiles)
	renderPreflightObligations(&b, t)

	fmt.Fprintf(&b, "\n## Required Behavior\n\n")
	fmt.Fprintf(&b, "- Read relevant files before editing.\n")
	fmt.Fprintf(&b, "- Keep edits inside allowed paths.\n")
	fmt.Fprintf(&b, "- If requirements are ambiguous, choose the smallest reversible implementation, record the decision, and continue.\n")
	fmt.Fprintf(&b, "- Run the highest-value verification available in this environment.\n")
	fmt.Fprintf(&b, "- Return exactly one JSON object matching the configured schema.\n")

	return b.String()
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

func renderReviewContext(b *strings.Builder, t Task) {
	var requeueInstructions []Risk
	var otherRisks []Risk
	for _, risk := range t.Risks {
		if strings.HasPrefix(risk.ID, "requeue-") {
			requeueInstructions = append(requeueInstructions, risk)
			continue
		}
		otherRisks = append(otherRisks, risk)
	}
	if t.PR.URL != "" || len(requeueInstructions) > 0 {
		fmt.Fprintf(b, "## PR Review Context\n\n")
		if t.PR.URL != "" {
			fmt.Fprintf(b, "- PR: `%s`\n", t.PR.URL)
		}
		if t.Supervisor.ReviewIterations > 0 {
			fmt.Fprintf(b, "- review iteration: `%d`\n", t.Supervisor.ReviewIterations)
		}
		for _, instruction := range requeueInstructions {
			fmt.Fprintf(b, "- additional instruction `%s`: %s\n", instruction.ID, instruction.Detail)
		}
		fmt.Fprintf(b, "\n")
	}
	if len(t.RevisionRequests) > 0 {
		fmt.Fprintf(b, "## Revision Requests\n\n")
		fmt.Fprintf(b, "Treat every pending revision request as an additional acceptance criterion for this attempt. Do not report `completed` unless each pending request is addressed or explicitly escalated with a reason.\n\n")
		for _, request := range t.RevisionRequests {
			if request.Status == "addressed" {
				continue
			}
			fmt.Fprintf(b, "- `%s` source=`%s`", request.ID, request.Source)
			if request.CommentID != "" {
				fmt.Fprintf(b, " comment=`%s`", request.CommentID)
			}
			fmt.Fprintf(b, ": %s\n", request.Text)
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
// kinds, purposes, and checkpoint commands derived from the task YAML
// declarations. The daemon may further augment this with the runtime
// preflight result; this static rendering is what `galley task work-order`
// prints offline.
func renderPreflightObligations(b *strings.Builder, t Task) {
	if t.Preflight == nil || t.Preflight.AcceptanceSkeleton == nil || !t.Preflight.AcceptanceSkeleton.IsEnabled() {
		return
	}
	fmt.Fprintf(b, "\n## Acceptance Skeleton Obligations\n\n")
	cfg := t.Preflight.AcceptanceSkeleton
	if len(cfg.Outputs) == 0 {
		fmt.Fprintf(b, "Galley will pre-create AC-linked test skeletons in the worktree before this attempt. Read each skeleton, complete the implementation it verifies, and ensure its checkpoint command would pass. Do not delete skeleton files or weaken their assertions to satisfy them.\n")
		return
	}
	fmt.Fprintf(b, "Galley will pre-create the following AC-linked test skeletons in the worktree before this attempt. Read each skeleton, complete the implementation it verifies, and ensure its checkpoint command would pass.\n\n")
	for _, out := range cfg.Outputs {
		fmt.Fprintf(b, "- AC `%s` -> `%s` (kind=%s, implementation_required=%t)\n", out.ACID, out.Path, out.Kind, out.ImplementationRequired)
		fmt.Fprintf(b, "  Purpose: %s\n", out.Purpose)
		fmt.Fprintf(b, "  Checkpoint: `%s`\n", out.CheckpointCommand)
	}
	fmt.Fprintf(b, "\nCompletion obligations: every implementation_required skeleton above must have a passing checkpoint before the supervisor accepts the attempt.\n")
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
		fmt.Fprintf(b, "- network: `%s`\n", profiles.Environment.Constraints.Network)
		fmt.Fprintf(b, "- secrets policy: `%s`\n", profiles.Environment.Constraints.SecretsPolicy)
		fmt.Fprintf(b, "- destructive commands: `%s`\n", profiles.Environment.Constraints.DestructiveCommands)
	}
}
