package daemon

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/shinpr/galley/internal/executorflow"
	skeletonpreflight "github.com/shinpr/galley/internal/preflight/skeleton"
	"github.com/shinpr/galley/internal/profile"
	"github.com/shinpr/galley/internal/runartifact"
	"github.com/shinpr/galley/internal/runner"
	"github.com/shinpr/galley/internal/task"
	"github.com/shinpr/galley/internal/workspace"
)

type attemptOutcome struct {
	Started      time.Time
	Completed    time.Time
	RunResult    runner.RunResult
	RunErr       error
	ClaudeResult runner.ClaudeResult
	ParseErr     error
	DiffDirty    bool
	Diff         string
	DiffErr      error
	// DiffSnapshot retains the full git evidence for this attempt so progress
	// detection can decide whether the dirty diff contains non-skeleton
	// changes. The snapshot is a value rather than a pointer so an empty
	// outcome stays a zero struct.
	DiffSnapshot workspace.Snapshot
}

func runExecutorAttempt(ctx context.Context, opts Options, loaded task.Task, profiles profile.Bundle, workDir, baseSHA, attemptDir, prompt, taskFile string, preflight *skeletonpreflight.Result) (attemptOutcome, error) {
	attemptCtx := ctx
	var cancel context.CancelFunc
	attemptTimeout := time.Duration(loaded.ExecutionPolicy.TimeoutMS) * time.Millisecond
	if attemptTimeout > 0 {
		attemptCtx, cancel = context.WithTimeout(ctx, attemptTimeout)
		defer cancel()
	}

	cli := loaded.Executor.CLI
	if cli == "" {
		return attemptOutcome{}, fmt.Errorf("executor.cli is required")
	}

	var (
		commandPlan runner.Command
		stdoutPath  string
		stderrPath  string
		err         error
	)
	switch cli {
	case "claude":
		commandPlan, stdoutPath, stderrPath, err = prepareClaudeExecutorPlan(opts, loaded, workDir, prompt, attemptDir)
	case "glm":
		commandPlan, stdoutPath, stderrPath, err = prepareGLMExecutorPlan(opts, loaded, workDir, prompt, attemptDir)
	case "codex":
		commandPlan, stdoutPath, stderrPath, err = prepareCodexExecutorPlan(opts, loaded, workDir, prompt, attemptDir)
	default:
		return attemptOutcome{}, fmt.Errorf("unsupported executor.cli %q; must be one of: %s", cli, strings.Join(task.ExecutorCLIEnum(), ", "))
	}
	if err != nil {
		return attemptOutcome{}, err
	}
	run, err := executorflow.RunCommandAttempt(attemptCtx, executorflow.CommandAttemptOptions{
		AttemptDir:  attemptDir,
		CommandPlan: commandPlan,
		Timeout:     attemptTimeout,
		IdleTimeout: opts.IdleTimeout,
		StdoutPath:  stdoutPath,
		StderrPath:  stderrPath,
	})
	if err != nil {
		return attemptOutcome{}, err
	}

	resultPath := runartifact.Path(attemptDir, runartifact.ExecutorResultFilename)
	lastMessagePath := codexLastMessagePath(cli, attemptDir)
	claudeResult, parseErr := resolveExecutorResult(cli, stdoutPath, run.RunResult.Stdout, lastMessagePath)
	if parseErr == nil {
		if err := writeJSON(resultPath, claudeResult); err != nil {
			return attemptOutcome{}, err
		}
	}

	// Stage executor-produced worktree changes before capturing the snapshot
	// Galley hands to the supervisor. Without this step, newly-created
	// untracked files would not appear in the staged or unstaged diff surfaces
	// and the supervisor would receive an empty diff for new-file work. Non-committed task input file destinations are excluded so
	// the staged review evidence is constrained to executor-produced changes
	// and context-only inputs do not leak into the supervisor diff. Staging failure is fatal: we surface
	// a typed error so the caller records a `review_staging` attempt failure
	// instead of sending an empty diff to the supervisor. The parent
	// ctx (not attemptCtx) is used here so a staging step initiated after
	// executor timeout still has a chance to capture worktree state and write
	// its evidence file.
	excludePaths := nonCommittedInputDestinations(loaded.Files)
	if err := stageExecutorOutput(ctx, opts, workDir, attemptDir, excludePaths); err != nil {
		return attemptOutcome{}, &reviewStagingError{Err: err}
	}

	diffArtifacts, err := executorflow.CaptureDiffArtifacts(ctx, workDir, baseSHA, attemptDir, workspaceOptions(opts))
	if err != nil {
		return attemptOutcome{}, err
	}

	return attemptOutcome{
		Started:      run.Started,
		Completed:    run.Completed,
		RunResult:    run.RunResult,
		RunErr:       run.RunErr,
		ClaudeResult: claudeResult,
		ParseErr:     parseErr,
		DiffDirty:    diffArtifacts.Dirty,
		Diff:         diffArtifacts.Diff,
		DiffErr:      diffArtifacts.Err,
		DiffSnapshot: diffArtifacts.Snapshot,
	}, nil
}

func markRevisionRequestsAddressed(loaded *task.Task, evidence string) {
	for i := range loaded.RevisionRequests {
		if loaded.RevisionRequests[i].Status == "addressed" {
			continue
		}
		loaded.RevisionRequests[i].Status = "addressed"
		loaded.RevisionRequests[i].Evidence = evidence
	}
}

func mergeAttemptEvidence(loaded *task.Task, outcome attemptOutcome, runID, workDir, attemptDir string) {
	loaded.Attempts = append(loaded.Attempts, task.Attempt{
		Number:            len(loaded.Attempts) + 1,
		StartedAt:         outcome.Started.Format(time.RFC3339Nano),
		CompletedAt:       outcome.Completed.Format(time.RFC3339Nano),
		ClaudeStatus:      claudeStatus(outcome.RunResult, outcome.RunErr),
		SupervisorVerdict: "not_reviewed",
		Summary:           fmt.Sprintf("Executor run %s; run_id=%s; workspace=%s", claudeStatus(outcome.RunResult, outcome.RunErr), runID, workDir),
		Error:             executorAttemptError(outcome, attemptDir),
	})
	loaded.Verification.Commands = append(loaded.Verification.Commands, task.VerificationCommand{
		Cmd:           executorVerificationCmd(loaded.Executor.CLI),
		Status:        verificationStatus(outcome.RunErr),
		OutputExcerpt: fmt.Sprintf("executor stdout/stderr captured under %s; run_result.json contains bounded tails", attemptDir),
	})
	if outcome.DiffErr != nil {
		appendRisk(loaded, "git-diff", "partial_verification", outcome.DiffErr.Error(), "Stored other run evidence; git diff evidence is unavailable.", true)
	}
	if outcome.ParseErr != nil {
		appendRisk(loaded, "claude-result-parse", "partial_verification", outcome.ParseErr.Error(), "Stored raw Claude stdout and stderr for supervisor review.", true)
		return
	}
	if outcome.ClaudeResult.Status == "completed" && outcome.DiffErr == nil && !outcome.DiffDirty {
		appendRisk(loaded, "git-diff-empty", "partial_verification", "Executor completed but produced no git diff in the execution workspace.", "Stored Claude result and raw logs for supervisor review.", true)
	}
	for _, ac := range outcome.ClaudeResult.AcceptanceCriteria {
		for i := range loaded.AcceptanceCriteria {
			if loaded.AcceptanceCriteria[i].ID == ac.ID {
				loaded.AcceptanceCriteria[i].Status = mapAcceptanceStatus(ac.Status)
			}
		}
	}
	for _, verification := range outcome.ClaudeResult.Verification {
		loaded.Verification.Commands = append(loaded.Verification.Commands, task.VerificationCommand{
			Cmd:           verification.Command,
			Status:        verification.Status,
			OutputExcerpt: verification.OutputExcerpt,
		})
	}
	for _, decision := range outcome.ClaudeResult.Decisions {
		loaded.Decisions = append(loaded.Decisions, task.Decision{
			ID:               fmt.Sprintf("claude-decision-%d", len(loaded.Decisions)+1),
			Question:         decision.Question,
			Chosen:           decision.Chosen,
			Rationale:        decision.Rationale,
			Reversibility:    decision.Reversibility,
			NeedsHumanReview: decision.NeedsHumanReview,
		})
	}
	for _, claudeRisk := range outcome.ClaudeResult.Risks {
		appendRisk(loaded, "claude-risk", claudeRisk.Type, claudeRisk.Detail, claudeRisk.Mitigation, claudeRisk.NeedsHumanReview)
	}
	if outcome.ClaudeResult.Status == "hard_stop" && outcome.ClaudeResult.HardStop != nil {
		appendRisk(loaded, "claude-hard-stop", "other", outcome.ClaudeResult.HardStop.Reason, strings.Join(outcome.ClaudeResult.HardStop.NeededToContinue, "; "), true)
	}
}

// executorVerificationCmd returns a stable command label that identifies the
// executor CLI used for an attempt. It is the value Galley records in
// task.verification.commands so reviewers can tell whether the run was driven
// by Claude or Codex from the saved task file alone.
func executorVerificationCmd(cli string) string {
	switch cli {
	case "codex":
		return "codex exec"
	case "glm":
		// glm drives the Claude binary against GLM's endpoint; label it so
		// reviewers can tell the run used GLM from the saved task file alone.
		return "claude -p (glm)"
	case "", "claude":
		return "claude -p"
	default:
		return "unknown"
	}
}

func executorAttemptError(outcome attemptOutcome, attemptDir string) *task.AttemptError {
	if outcome.RunErr == nil {
		return nil
	}
	return attemptError("executor", classifyFailureKind("executor_failed", outcome.RunErr), outcome.RunErr, attemptDir)
}

func appendSupervisorFailureAttempt(loaded *task.Task, outcome attemptOutcome, err error, attemptDir string) {
	// An exhausted supervisor idle timeout is an infrastructure
	// watchdog failure, not a supervisor verdict. Record it under the distinct
	// supervisor_idle_timeout kind with a self-explanatory message instead of
	// the generic supervisor-failure classification.
	if idle, ok := asSupervisorIdleTimeout(err); ok {
		appendSupervisorIdleTimeoutAttempt(loaded, outcome, idle, attemptDir)
		return
	}
	kind := classifyFailureKind("supervisor_failed", err)
	loaded.Attempts = append(loaded.Attempts, task.Attempt{
		Number:            len(loaded.Attempts) + 1,
		StartedAt:         outcome.Started.Format(time.RFC3339Nano),
		CompletedAt:       time.Now().UTC().Format(time.RFC3339Nano),
		ClaudeStatus:      claudeStatus(outcome.RunResult, outcome.RunErr),
		SupervisorVerdict: kind,
		Summary:           err.Error(),
		Error:             attemptError("supervisor", kind, err, attemptDir),
	})
	// A supervisor evaluation that fails after exhausting the in-attempt retry
	// budget (idle timeout, total timeout, or forced kill) is a transient
	// runtime failure of the supervisor process, not a defect in the task or
	// the executor's work. Surface it as needs_supervisor_review — consistent
	// with the loop's other "a human should look at this" outcomes — so the
	// task moves to failed/ with a status that signals follow-up review rather
	// than a hard task failure.
	loaded.Status = "needs_supervisor_review"
	appendRisk(loaded, "supervisor-stall", "partial_verification", fmt.Sprintf("Supervisor evaluation failed (%s): %s", kind, err.Error()), "Inspect the supervisor-try-N evidence under the attempt directory and requeue the task once the supervisor backend is healthy.", true)
}

// appendSupervisorIdleTimeoutAttempt records the failed attempt for an
// exhausted built-in supervisor idle timeout. The attempt error uses the
// distinct supervisor_idle_timeout kind and a message that names the
// supervisor adapter, idle-timeout duration, and try count, so the failed task
// YAML and `galley task show` explain the failure without daemon logs
// . The SupervisorVerdict field is set to the same infrastructure
// kind rather than needs_revision or accepted because no supervisor verdict
// was produced. Task lifecycle stays identical to the existing
// supervisor-stall path: needs_supervisor_review, then moved to failed/.
func appendSupervisorIdleTimeoutAttempt(loaded *task.Task, outcome attemptOutcome, idle *supervisorIdleTimeoutError, attemptDir string) {
	message := idle.attemptErrorMessage()
	loaded.Attempts = append(loaded.Attempts, task.Attempt{
		Number:            len(loaded.Attempts) + 1,
		StartedAt:         outcome.Started.Format(time.RFC3339Nano),
		CompletedAt:       time.Now().UTC().Format(time.RFC3339Nano),
		ClaudeStatus:      claudeStatus(outcome.RunResult, outcome.RunErr),
		SupervisorVerdict: supervisorIdleTimeoutKind,
		Summary:           message,
		Error: &task.AttemptError{
			Phase:       "supervisor",
			Kind:        supervisorIdleTimeoutKind,
			Message:     message,
			ArtifactDir: attemptDir,
		},
	})
	loaded.Status = "needs_supervisor_review"
	appendRisk(loaded, "supervisor-idle-timeout", "partial_verification", message, "Inspect the supervisor-try-N evidence under the attempt directory, then requeue the task or adjust the daemon --idle-timeout or --supervisor settings.", true)
}
