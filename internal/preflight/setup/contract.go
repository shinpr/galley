package setup

import (
	"fmt"
	"strings"
)

// setupExecutorFailureResult builds a structured setup failure record with
// enough evidence for an operator to repair environment.setup. When the
// failure surfaced from an actual setup executor invocation the caller passes
// the executor run identifier, exit code, stdout/stderr, and inspected files.
// When the failure occurred before the executor could run (request marshaling,
// command plan construction) the executor-specific arguments may be zero
// values and only the message is recorded.
func setupExecutorFailureResult(message, provider, executorRun string, exitCode int, stdout, stderr string, inspected []string) *Result {
	res := &Result{
		Status:         StatusFailed,
		Commands:       []CommandAttempt{},
		Error:          message,
		Provider:       provider,
		Source:         SourceDiscovered,
		InspectedFiles: append([]string{}, inspected...),
		RepairGuidance: "Inspect runs/<run-id>/setup_executor.stderr.log and runs/<run-id>/setup_executor.stdout.jsonl; ensure environment.setup or environment.commands provides a working plan, then requeue.",
	}
	if executorRun != "" {
		res.Commands = append(res.Commands, CommandAttempt{
			Run:           executorRun,
			Why:           "setup executor invocation",
			Source:        SourceDiscovered,
			ExitCode:      exitCode,
			StdoutExcerpt: truncateExcerpt(stdout),
			StderrExcerpt: truncateExcerpt(stderr),
		})
	}
	return res
}

func setupNotReadyError(res *Result) error {
	if res == nil {
		return fmt.Errorf("setup executor did not make the worktree ready")
	}
	parts := []string{"setup executor did not make the worktree ready"}
	if msg := strings.TrimSpace(res.Error); msg != "" {
		parts = append(parts, msg)
	}
	if guidance := strings.TrimSpace(res.RepairGuidance); guidance != "" {
		parts = append(parts, "repair: "+guidance)
	}
	return fmt.Errorf("%s", strings.Join(parts, ": "))
}

// EnforceLearnedPlanContract validates that a setup executor that returned
// status=ready also returned the evidence and successful plan Galley needs to
// persist setup readiness for the next task.
func EnforceLearnedPlanContract(res *Result) error {
	if res == nil || res.Status != StatusReady {
		return nil
	}
	if len(res.SuccessfulCommands) == 0 {
		return fmt.Errorf("setup executor returned status=ready with no successful_commands; cannot learn a setup plan to persist to environment.yaml")
	}
	if err := validateSuccessfulSetupCommands(res); err != nil {
		return err
	}
	if strings.TrimSpace(res.ReadinessEvidence) == "" {
		return fmt.Errorf("setup executor returned status=ready with no readiness_evidence")
	}
	switch res.Source {
	case SourceEnvironmentSetup, SourceEnvironmentCommands, SourceDiscovered:
		return nil
	case "":
		return fmt.Errorf("setup executor returned status=ready with no source")
	default:
		return fmt.Errorf("setup executor returned status=ready with invalid source %q", res.Source)
	}
}

func validateSuccessfulSetupCommands(res *Result) error {
	hasSuccessfulAttempt := false
	for _, cmd := range res.Commands {
		if cmd.ExitCode == 0 && cmd.Source != SourceReadinessCheck {
			hasSuccessfulAttempt = true
			break
		}
	}
	if !hasSuccessfulAttempt {
		return fmt.Errorf("setup executor returned status=ready with no successful setup command attempt")
	}
	for i, cmd := range res.SuccessfulCommands {
		run := strings.TrimSpace(cmd.Run)
		if run == "" {
			return fmt.Errorf("setup executor returned status=ready with empty successful_commands[%d].run", i)
		}
	}
	return nil
}

// applySetupContractViolation downgrades a result that violated the
// learned-plan contract from ready to failed and ensures repair guidance is
// present so the saved setup_result.json carries enough detail for the
// operator to fix environment.commands or author environment.setup before
// requeuing.
func applySetupContractViolation(res *Result, err error) {
	if res == nil || err == nil {
		return
	}
	markSetupFailed(res, err.Error(), "Return the ordered successful_commands the setup executor ran, then requeue. environment.yaml was intentionally left unchanged to avoid silently dropping a stale or unknown setup plan.")
}

func markSetupFailed(res *Result, message, guidance string) {
	if res == nil {
		return
	}
	res.Status = StatusFailed
	if res.Error == "" {
		res.Error = message
	}
	if res.RepairGuidance == "" {
		res.RepairGuidance = guidance
	}
}
