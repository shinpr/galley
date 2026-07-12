package setup

import (
	"context"
	"errors"
	"fmt"
)

// Run orchestrates the setup phase. It returns the result
// (nil when no setup work was needed and the daemon should skip the stage),
// an optional environment update record, and an error. The error is non-nil
// only on hard preflight failures (Galley-side I/O, contract violations, a
// setup executor that failed to produce a ready worktree, or a learned-plan
// persistence failure that means cannot be honored for the next task).
func Run(ctx context.Context, opts Options) (*Result, *EnvironmentUpdate, error) {
	if opts.RunDir == "" {
		return nil, nil, fmt.Errorf("setup preflight: run dir is required")
	}
	if opts.WorkDir == "" {
		return nil, nil, fmt.Errorf("setup preflight: work dir is required")
	}
	if opts.Profiles.Environment == nil {
		// Without an environment profile there is no setup contract to enforce
		// and no commands map to learn from. Skip the stage so existing tasks
		// without an environment profile keep the prior daemon behavior.
		return nil, nil, nil
	}

	env := opts.Profiles.Environment
	runner := opts.ExecutorRunner
	if runner == nil {
		runner = RunExecutor
	}
	res, err := runner(ctx, opts)
	// Stamp the resolved identity used for this invocation so requeue reuse can
	// invalidate when environment or task resolution changes later.
	ApplyExecutorIdentity(res, opts.Task.Executor)
	if writeErr := WriteResult(opts.RunDir, res); writeErr != nil && err == nil {
		err = writeErr
	}
	if err != nil {
		return res, nil, err
	}
	if res == nil || res.Status != StatusReady {
		return res, nil, setupNotReadyError(res)
	}
	// Discovery reported ready: enforce the learned-plan contract before
	// persistence so a ready+empty-successful_commands response cannot silently
	// leave environment.yaml unchanged.
	if vErr := EnforceLearnedPlanContract(res); vErr != nil {
		applySetupContractViolation(res, vErr)
		_ = WriteResult(opts.RunDir, res)
		return res, nil, fmt.Errorf("setup phase failed: %w", vErr)
	}
	// On success, persist the learned plan back to the environment profile when
	// the resolved profile lacked a setup field so subsequent tasks reuse
	// it without rediscovery. A failure to persist is treated as a setup-phase
	// failure: subsequent tasks must see the learned plan without rediscovery,
	// so silently swallowing the rewrite error would re-cost discovery every run.
	update, perr := persistLearnedSetupPlan(opts, env, res)
	if perr != nil {
		if writeErr := recordSetupProfileUpdateFailure(opts.RunDir, perr); writeErr != nil {
			perr = errors.Join(perr, writeErr)
		}
		if res != nil {
			markSetupFailed(res, "learned setup plan persistence failed: "+perr.Error(), "Inspect setup_result.json and environment_update.json, fix environment.yaml, and requeue the task.")
			_ = WriteResult(opts.RunDir, res)
		}
		return res, nil, fmt.Errorf("setup phase failed: persist learned setup plan: %w", perr)
	}
	if update != nil {
		if err := WriteEnvironmentUpdate(opts.RunDir, update); err != nil {
			return res, nil, err
		}
	}
	return res, update, nil
}

// RunExecutor dispatches the setup executor for the selected effective
// provider transport (claude, codex, glm, or grok) to attempt to make the
// worktree ready.
func RunExecutor(ctx context.Context, opts Options) (*Result, error) {
	signals := opts.RepositorySignals
	if signals == nil {
		signals = DiscoverRepositorySignals(opts.WorkDir)
	}
	payload, perr := marshalSetupExecutorRequest(opts, signals)
	if perr != nil {
		return setupExecutorFailureResult("plan setup executor request: "+perr.Error(), "", "", 0, "", "", signals), perr
	}
	commandPlan, provider, perr := BuildExecutorCommandPlan(opts, payload)
	if perr != nil {
		return setupExecutorFailureResult("plan setup executor command: "+perr.Error(), provider, "", 0, "", "", signals), perr
	}
	if err := writeSetupExecutorCommandPlan(opts.RunDir, commandPlan); err != nil {
		return setupExecutorFailureResult("write setup executor command plan: "+err.Error(), provider, "", 0, "", "", signals), err
	}
	out, runErr := RunExecutorCommand(ctx, opts, commandPlan)
	executorRun := fmt.Sprintf("<setup_executor:%s>", provider)
	parsed, parseErr := ResolveExecutorResult(opts, out.Stdout)
	if parseErr != nil {
		message := "setup executor did not return a valid result: " + parseErr.Error()
		if runErr != nil {
			message = fmt.Sprintf("setup executor exited %d: %s", out.ExitCode, truncateExcerpt(out.Stderr))
		}
		failure := setupExecutorFailureResult(message, provider, executorRun, out.ExitCode, out.Stdout, out.Stderr, signals)
		return failure, errors.Join(fmt.Errorf("setup executor failed: %w", parseErr), runErr)
	}
	parsed.Provider = provider
	if parsed.Status == "" {
		parsed.Status = StatusFailed
	}
	if runErr != nil && parsed.Status == StatusReady {
		// Defensive: the runner reported a non-zero exit but the executor
		// declared ready. Trust the runner; readiness without exit 0 is not
		// trustworthy.
		markSetupFailed(parsed, fmt.Sprintf("setup executor process exited non-zero: %v", runErr), "Inspect setup_executor.stderr.log and setup_executor.stdout.jsonl, fix the setup executor runtime failure, and requeue the task.")
	}
	return parsed, nil
}
