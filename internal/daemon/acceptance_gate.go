package daemon

import (
	"fmt"
	"strings"

	skeletonpreflight "github.com/shinpr/galley/internal/preflight/skeleton"
	"github.com/shinpr/galley/internal/supervisor"
	"github.com/shinpr/galley/internal/task"
)

// AcceptanceGateInputs are the values the daemon-side accept gate inspects
// before acceptSupervisorVerdict finalizes the task.
type AcceptanceGateInputs struct {
	Required      bool
	Outputs       []skeletonpreflight.Output
	NoSkeletons   []skeletonpreflight.NoOutput
	AcceptanceIDs []string
}

// AcceptanceGate rejects acceptance when required skeleton coverage is missing.
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

func evaluateAcceptanceGate(loaded *task.Task, runDir string) (string, bool) {
	if loaded == nil || loaded.Preflight == nil || loaded.Preflight.AcceptanceSkeleton == nil {
		return "", true
	}
	cfg := loaded.Preflight.AcceptanceSkeleton
	if !cfg.IsEnabled() {
		return "", true
	}
	res, err := skeletonpreflight.LoadResult(runDir)
	if err != nil {
		return fmt.Sprintf("could not read preflight_result.json: %v", err), false
	}
	if res == nil {
		return "preflight_result.json is missing for an enabled acceptance skeleton task", false
	}
	if res.Status == "failed" {
		message := "acceptance skeleton preflight failed"
		if res.Error != nil && res.Error.Message != "" {
			message = "acceptance skeleton preflight failed: " + res.Error.Message
		}
		return message, false
	}
	if res.Status == "skipped" {
		if cfg.IsRequired() {
			return "acceptance skeleton preflight was skipped while required", false
		}
		return "", true
	}
	acceptanceIDs := make([]string, 0, len(loaded.AcceptanceCriteria))
	for _, ac := range loaded.AcceptanceCriteria {
		acceptanceIDs = append(acceptanceIDs, ac.ID)
	}
	reason, ok := AcceptanceGate(AcceptanceGateInputs{
		Required:      cfg.IsRequired(),
		Outputs:       res.Outputs,
		NoSkeletons:   res.NoSkeletons,
		AcceptanceIDs: acceptanceIDs,
	})
	return reason, ok
}

func mapAcceptanceStatus(status string) string {
	switch status {
	case "satisfied", "partially_satisfied", "not_satisfied":
		return status
	default:
		return "unknown"
	}
}

// applyAcceptedAcceptanceCriteria prevents stale executor statuses from
// contradicting an accepted supervisor verdict while preserving reported gaps.
func applyAcceptedAcceptanceCriteria(loaded *task.Task, verdict supervisor.Verdict) {
	if verdict.Status != "accepted" {
		return
	}
	gaps := make(map[string]bool, len(verdict.AcceptanceGaps))
	for _, id := range verdict.AcceptanceGaps {
		gaps[strings.TrimSpace(id)] = true
	}
	for i := range loaded.AcceptanceCriteria {
		ac := &loaded.AcceptanceCriteria[i]
		if gaps[ac.ID] {
			ac.Status = "partially_satisfied"
			continue
		}
		ac.Status = "satisfied"
	}
}
