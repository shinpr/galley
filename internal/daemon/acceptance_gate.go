package daemon

import (
	"fmt"
	"strings"

	skeletonpreflight "github.com/shinpr/galley/internal/preflight/skeleton"
)

// AcceptanceGateInputs are the values the daemon-side accept gate inspects
// before acceptSupervisorVerdict finalizes the task.
type AcceptanceGateInputs struct {
	Required      bool
	Outputs       []skeletonpreflight.Output
	NoSkeletons   []skeletonpreflight.NoOutput
	AcceptanceIDs []string
}

// AcceptanceGate enforces : an accepted verdict must be downgraded to
// needs_supervisor_review when required skeleton coverage is missing. The
// supervisor is responsible for inspecting implementation_required skeletons
// for TODO/skipped/placeholder tests; required test execution evidence comes
// from the normal quality profile checks.
func AcceptanceGate(in AcceptanceGateInputs) (string, bool) {
	if !in.Required {
		return "", true
	}

	covered := map[string]bool{}
	for _, out := range in.Outputs {
		covered[out.ACID] = true
	}
	for _, ns := range in.NoSkeletons {
		covered[ns.ACID] = true
	}
	var problems []string
	for _, id := range in.AcceptanceIDs {
		if !covered[id] {
			problems = append(problems, fmt.Sprintf("AC %s has no skeleton output and no no_skeletons reason", id))
		}
	}
	if len(problems) == 0 {
		return "", true
	}
	return strings.Join(problems, "; "), false
}
