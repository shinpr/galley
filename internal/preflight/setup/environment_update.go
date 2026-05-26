package setup

import (
	"fmt"
	"strings"
	"time"

	"github.com/shinpr/galley/internal/profile"
)

// persistLearnedSetupPlan writes the successful setup plan back to the
// repository environment profile when the resolved profile lacked a setup
// field or the learned plan differs from the existing one. The rewrite is
// atomic and validates the result before returning.
func persistLearnedSetupPlan(opts Options, env *profile.Environment, res *Result) (*EnvironmentUpdate, error) {
	if res == nil || res.Status != StatusReady {
		return nil, nil
	}
	if len(res.SuccessfulCommands) == 0 {
		return nil, nil
	}
	if opts.EnvironmentProfilePath == "" {
		return nil, nil
	}
	plan := profile.SetupPlan{Commands: append([]profile.SetupCommand{}, res.SuccessfulCommands...)}
	if env.Setup != nil && setupPlansEqual(*env.Setup, plan) {
		return nil, nil
	}
	prior, err := profile.UpdateEnvironmentSetup(opts.EnvironmentProfilePath, plan)
	if err != nil {
		return nil, err
	}
	reason := "no setup field; persisted learned plan"
	if prior != nil {
		reason = "learned plan differs from prior setup; updated"
	}
	return &EnvironmentUpdate{
		ProfilePath: opts.EnvironmentProfilePath,
		Changed:     true,
		Before:      prior,
		After:       plan,
		Diff:        setupPlanDiff(prior, plan),
		Reason:      reason,
		UpdatedAt:   time.Now().UTC().Format(time.RFC3339Nano),
	}, nil
}

func setupPlanDiff(before *profile.SetupPlan, after profile.SetupPlan) string {
	var b strings.Builder
	b.WriteString("environment.setup.commands\n")
	if before == nil || len(before.Commands) == 0 {
		b.WriteString("- <absent>\n")
	} else {
		for _, cmd := range before.Commands {
			fmt.Fprintf(&b, "- run: %q", cmd.Run)
			if cmd.Why != "" {
				fmt.Fprintf(&b, " why: %q", cmd.Why)
			}
			b.WriteString("\n")
		}
	}
	for _, cmd := range after.Commands {
		fmt.Fprintf(&b, "+ run: %q", cmd.Run)
		if cmd.Why != "" {
			fmt.Fprintf(&b, " why: %q", cmd.Why)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func setupPlansEqual(a, b profile.SetupPlan) bool {
	if len(a.Commands) != len(b.Commands) {
		return false
	}
	for i := range a.Commands {
		if strings.TrimSpace(a.Commands[i].Run) != strings.TrimSpace(b.Commands[i].Run) {
			return false
		}
	}
	return true
}
