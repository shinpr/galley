package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	skeletonpreflight "github.com/shinpr/galley/internal/preflight/skeleton"
	"github.com/shinpr/galley/internal/profile"
	"github.com/shinpr/galley/internal/runartifact"
	"github.com/shinpr/galley/internal/runlog"
	"github.com/shinpr/galley/internal/runner"
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
	if reason, ok := requiredCheckEvidenceGate(loaded, runDir); !ok {
		return reason, false
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

// requiredCheckEvidenceGate treats preferred commands as fallbacks: one pass
// satisfies a check, while failures block only when no fallback passes.
func requiredCheckEvidenceGate(loaded *task.Task, runDir string) (string, bool) {
	if runDir == "" {
		return "", true
	}
	profiles, err := loadRunProfiles(runDir)
	if err != nil || profiles.Quality == nil {
		return "", true
	}
	var required []profile.RequiredCheck
	for _, c := range profiles.Quality.RequiredChecks {
		if c.Required {
			required = append(required, c)
		}
	}
	if len(required) == 0 {
		return "", true
	}
	res, _, err := loadLatestExecutorResult(runDir)
	if err != nil || res == nil {
		return "no executor result is available to verify required quality checks", false
	}
	// Later retry evidence supersedes earlier results for the same command.
	status := map[string]string{}
	for _, v := range res.Verification {
		status[strings.TrimSpace(v.Command)] = strings.TrimSpace(v.Status)
	}
	var problems []string
	for _, c := range required {
		if len(c.PreferredCommands) == 0 {
			problems = append(problems, fmt.Sprintf("required check %q declares no preferred commands", c.ID))
			continue
		}
		satisfied := false
		sawFailure := false
		sawAny := false
		var failed []string
		for _, cmd := range c.PreferredCommands {
			key := strings.TrimSpace(cmd)
			if key == "" {
				continue
			}
			switch status[key] {
			case "passed":
				satisfied = true
				sawAny = true
			case "":
			default:
				sawFailure = true
				sawAny = true
				failed = append(failed, key)
			}
		}
		switch {
		case satisfied:
		case sawFailure:
			problems = append(problems, fmt.Sprintf("required check %q has failed verification evidence for [%s] and no passing fallback command", c.ID, strings.Join(failed, ", ")))
		case !sawAny:
			problems = append(problems, fmt.Sprintf("required check %q has no verification evidence for any of its preferred commands", c.ID))
		}
	}
	if len(problems) == 0 {
		return "", true
	}
	return strings.Join(problems, "; "), false
}

func loadRunProfiles(runDir string) (profile.Bundle, error) {
	data, err := os.ReadFile(runartifact.Path(runDir, runartifact.ProfilesFilename))
	if err != nil {
		return profile.Bundle{}, err
	}
	var payload struct {
		Bundle profile.Bundle `json:"bundle"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return profile.Bundle{}, err
	}
	return payload.Bundle, nil
}

func loadLatestExecutorResult(runDir string) (*runner.ExecutorResult, string, error) {
	bestDir, _, err := runlog.LatestAttemptDir(runDir)
	if err != nil {
		return nil, "", err
	}
	if bestDir == "" {
		return nil, "", nil
	}
	data, err := readExecutorResultFile(bestDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, bestDir, nil
		}
		return nil, bestDir, err
	}
	var res runner.ExecutorResult
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, bestDir, err
	}
	return &res, bestDir, nil
}

func readExecutorResultFile(attemptDir string) ([]byte, error) {
	return os.ReadFile(runartifact.Path(attemptDir, runartifact.ExecutorResultFilename))
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
