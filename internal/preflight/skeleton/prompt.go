package skeleton

import (
	"fmt"
	"strings"
)

func AppendObligations(prompt string, res *Result) string {
	if res == nil {
		return prompt
	}
	var b strings.Builder
	b.WriteString("\n## Acceptance Skeleton Obligations (Runtime)\n\n")
	if res.Status == "failed" {
		fmt.Fprintf(&b, "Acceptance skeleton preflight failed (%s); see preflight_result.json for details.\n", failureMessage(res))
		return prompt + b.String()
	}
	if len(res.Outputs) == 0 {
		b.WriteString("Preflight enabled with no declared outputs. Add coverage if acceptance criteria require behavior tests.\n")
		return prompt + b.String()
	}
	b.WriteString("Galley pre-created the following AC-linked test skeletons in the worktree before this attempt. Read each skeleton, complete the implementation it verifies, and keep the assertions meaningful. Do not delete skeleton files, leave placeholder assertions, skip the tests, or weaken their assertions.\n\n")
	for _, out := range res.Outputs {
		fmt.Fprintf(&b, "- AC `%s` -> `%s` (kind=%s, implementation_required=%t)\n", out.ACID, out.Path, out.Kind, out.ImplementationRequired)
		fmt.Fprintf(&b, "  Purpose: %s\n", out.Purpose)
		if out.Satisfies != "" {
			fmt.Fprintf(&b, "  Satisfies: %s\n", out.Satisfies)
		}
		if out.IntegrationPoint != "" {
			fmt.Fprintf(&b, "  Integration point: %s\n", out.IntegrationPoint)
		}
	}
	b.WriteString("\nCompletion obligations: every implementation_required skeleton above must be implemented and covered by the normal required verification checks before the supervisor accepts the attempt.\n")
	return prompt + b.String()
}

func failureMessage(res *Result) string {
	if res == nil || res.Error == nil {
		return "unknown failure"
	}
	if res.Error.Phase != "" && res.Error.Message != "" {
		return res.Error.Phase + ": " + res.Error.Message
	}
	if res.Error.Message != "" {
		return res.Error.Message
	}
	return res.Error.Phase
}
