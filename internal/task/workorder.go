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
	var b strings.Builder

	fmt.Fprintf(&b, "# Galley Work Order\n\n")
	fmt.Fprintf(&b, "Task ID: `%s`\n", t.ID)
	fmt.Fprintf(&b, "Mode: `%s`\n", t.Mode)
	fmt.Fprintf(&b, "Supervisor: `%s` / `%s`\n\n", t.Supervisor.Provider, t.Supervisor.Mode)
	fmt.Fprintf(&b, "## Goal\n\n%s\n\n", t.Goal)

	fmt.Fprintf(&b, "## Acceptance Criteria\n\n")
	for _, ac := range t.AcceptanceCriteria {
		fmt.Fprintf(&b, "- `%s`: %s\n  Verification: %s\n", ac.ID, ac.Text, ac.Verification)
	}

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

	fmt.Fprintf(&b, "\n## Required Behavior\n\n")
	fmt.Fprintf(&b, "- Read relevant files before editing.\n")
	fmt.Fprintf(&b, "- Keep edits inside allowed paths.\n")
	fmt.Fprintf(&b, "- If requirements are ambiguous, choose the smallest reversible implementation, record the decision, and continue.\n")
	fmt.Fprintf(&b, "- Run the highest-value verification available in this environment.\n")
	fmt.Fprintf(&b, "- Return exactly one JSON object matching the configured schema.\n")

	return b.String()
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
