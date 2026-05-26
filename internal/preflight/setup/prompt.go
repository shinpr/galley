package setup

import (
	"fmt"
	"strings"
)

// AppendReadinessObligations adds the setup readiness facts (and any
// learned-plan update) to the work order so the implementation executor sees
// the same readiness evidence the supervisor will review.
func AppendReadinessObligations(prompt string, res *Result, update *EnvironmentUpdate) string {
	if res == nil {
		return prompt
	}
	var b strings.Builder
	b.WriteString("\n## Setup Readiness\n\n")
	fmt.Fprintf(&b, "Galley setup phase status: %s (commands attempted: %d).\n", res.Status, len(res.Commands))
	if res.Source != "" {
		fmt.Fprintf(&b, "Setup source: %s.\n", res.Source)
	}
	if res.Provider != "" {
		fmt.Fprintf(&b, "Setup provider: %s.\n", res.Provider)
	}
	if res.ReadinessEvidence != "" {
		fmt.Fprintf(&b, "Readiness evidence: %s\n", res.ReadinessEvidence)
	}
	if len(res.SuccessfulCommands) > 0 {
		b.WriteString("Successful setup plan (this is the plan the setup phase persisted):\n")
		for i, cmd := range res.SuccessfulCommands {
			fmt.Fprintf(&b, " %d. `%s`", i+1, cmd.Run)
			if cmd.Why != "" {
				fmt.Fprintf(&b, " - %s", cmd.Why)
			}
			b.WriteString("\n")
		}
	}
	if update != nil && update.Changed {
		fmt.Fprintf(&b, "Galley updated environment.yaml setup field at %s (%s).\n", update.ProfilePath, update.Reason)
	} else if res.Status == StatusReady {
		b.WriteString("Setup readiness was confirmed without changing environment.yaml.\n")
	}
	return prompt + b.String()
}
