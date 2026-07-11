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

// evaluateAcceptanceGate inspects the preflight result and required-check
// evidence and returns ("", true) when acceptance is allowed.
// Tasks without preflight.acceptance_skeleton enabled always pass — the
// gate (including the required quality-check evidence gate) only activates
// when a human opted the task into acceptance skeleton preflight via the
// task contract.
func evaluateAcceptanceGate(loaded *task.Task, runDir string) (string, bool) {
	// Default flow: a task that omits or disables preflight.acceptance_skeleton
	// must validate and finalize through the normal daemon path. The required
	// quality-check evidence gate is part of the acceptance skeleton contract,
	// so only an enabled preflight section opts a task in.
	if loaded == nil || loaded.Preflight == nil || loaded.Preflight.AcceptanceSkeleton == nil {
		return "", true
	}
	cfg := loaded.Preflight.AcceptanceSkeleton
	if !cfg.IsEnabled() {
		return "", true
	}
	// Required quality-check evidence gate. Scoped to
	// preflight-enabled tasks so a supervisor cannot finalize an accepted
	// verdict while a required profile check is missing or failed in the
	// latest executor result. This gate is tied to enabled:true, not
	// required:true; required:false only relaxes per-AC skeleton coverage.
	if reason, ok := requiredCheckEvidenceGate(loaded, runDir); !ok {
		return reason, false
	}
	res, err := skeletonpreflight.LoadResult(runDir)
	if err != nil {
		return fmt.Sprintf("could not read preflight_result.json: %v", err), false
	}
	if res == nil {
		// Enabled task without a recorded result must not finalize silently.
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
		// Required preflight cannot be silently skipped to acceptance; there is
		// no waiver hook.
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

// requiredCheckEvidenceGate inspects the latest executor result for the run
// and verifies that every required quality-profile check has passing
// verification evidence. It is only invoked by evaluateAcceptanceGate for
// tasks that enabled acceptance skeleton preflight; the default daemon flow
// never reaches it. It also returns ("", true) when there is no run
// directory, no resolved quality profile, or no required checks.
//
// Gate semantics deliberately mirror preferred_commands: they are an ordered
// fallback list, not an AND-list. The gate therefore treats a required check as
// satisfied when any preferred command has passing verification evidence, as
// failed when none passed but at least one failed, and as missing only when no
// preferred command has evidence. Requiring evidence for every preferred command
// would downgrade multi-command checks even when the first command passed.
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
	// Index the latest verification evidence by command. The same command can
	// appear more than once (e.g. retried after a fix); the last entry wins so
	// a passing rerun supersedes an earlier failure.
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
				// no evidence for this fallback command — expected when an
				// earlier command in the list already passed.
			default:
				sawFailure = true
				sawAny = true
				failed = append(failed, key)
			}
		}
		switch {
		case satisfied:
			// ok — a preferred command passed.
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

func loadLatestExecutorResult(runDir string) (*runner.ClaudeResult, string, error) {
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
	var res runner.ClaudeResult
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

// applyAcceptedAcceptanceCriteria normalizes per-criterion statuses once the
// supervisor has accepted the attempt. The supervisor verdict represents the
// final decision over the whole task, so any AC still marked as pending,
// unknown, or not_satisfied from earlier executor reports would otherwise leak
// into the rendered PR body and mislead reviewers. AC IDs that the supervisor
// flagged as gaps are rendered as partially_satisfied to preserve that nuance.
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
