package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shinpr/galley/internal/executorflow"
	"github.com/shinpr/galley/internal/inputfiles"
	setuppreflight "github.com/shinpr/galley/internal/preflight/setup"
	skeletonpreflight "github.com/shinpr/galley/internal/preflight/skeleton"
	"github.com/shinpr/galley/internal/profile"
	"github.com/shinpr/galley/internal/runartifact"
	"github.com/shinpr/galley/internal/runlog"
	"github.com/shinpr/galley/internal/runner"
	claudeguard "github.com/shinpr/galley/internal/runner/claude_guard_plugin"
	"github.com/shinpr/galley/internal/supervisor"
	"github.com/shinpr/galley/internal/task"
	"github.com/shinpr/galley/internal/taskstate"
	"github.com/shinpr/galley/internal/vcs"
	"github.com/shinpr/galley/internal/workspace"
)

// reviewStagingError signals that Galley's review-time `git add -A` step
// failed after the executor exited and before the supervisor evaluation
// would have been driven against an empty or stale diff. The loop
// classification path (runOneSupervisorAttempt) inspects this type to record
// the failure with a distinct `review_staging` phase / `review_staging_failed`
// kind instead of the generic executor failure classification.
type reviewStagingError struct{ Err error }

func (e *reviewStagingError) Error() string {
	if e == nil || e.Err == nil {
		return "review staging failed"
	}
	return e.Err.Error()
}

func (e *reviewStagingError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func asReviewStagingError(err error) (*reviewStagingError, bool) {
	var rse *reviewStagingError
	if errors.As(err, &rse) {
		return rse, true
	}
	return nil, false
}

// stageExecutorOutput is the package-level seam used by runExecutorAttempt to
// stage the executor-produced worktree changes Galley hands to the
// supervisor.
//
// It discovers the dirty worktree paths, builds the explicit reviewable path
// set (dropping empty/non-local entries, deduplicating, and excluding
// excludePaths — task.files entries declared commit:false), and stages exactly
// that set. Forbidden-path entries are intentionally kept so the finalize-time
// forbidden_paths gate still observes them, and the staged diff the supervisor
// reviews reflects the executor's submitted artifact and nothing else.
//
// Tests override this seam to inject deterministic failures and assert the
// exclude-list contract without spawning a real git process. The signature
// keeps excludePaths visible at the seam so failure-path tests can document
// the contract.
var stageExecutorOutput = func(ctx context.Context, opts Options, workDir, attemptDir string, excludePaths []string) error {
	bins := vcsBinaries(opts)
	statusZ, err := vcs.StatusPorcelainZ(ctx, bins, workDir)
	if err != nil {
		return err
	}
	reviewable := reviewablePathsFromStatus(statusZ, excludePaths)
	return vcs.StagePathsForReview(ctx, bins, workDir, attemptDir, reviewable)
}

const progressNoDiffThreshold = 2

// supervisorRetryBudget is the internal, fixed number of additional supervisor
// evaluations Galley runs inside a single executor attempt when the supervisor
// subprocess exits because of idle timeout, total timeout, or a forced
// subprocess kill. It is intentionally not exposed as a
// CLI flag, task YAML field, or profile field; supervisor stalls are treated
// as transient runtime failures, not user-tunable behavior.
const supervisorRetryBudget = 2

// supervisorTotalAttempts is the maximum number of supervisor invocations per
// executor attempt: the initial try plus supervisorRetryBudget retries.
const supervisorTotalAttempts = supervisorRetryBudget + 1

// supervisorRunner runs one supervisor evaluation against evidence using
// tryDir as the artifact directory. A package-level function variable keeps
// the retry orchestration testable without spawning a real codex/claude
// process.
var supervisorRunner = defaultSupervisorRunner

func defaultSupervisorRunner(ctx context.Context, opts Options, evidence supervisor.Evidence, tryDir, workDir string) (supervisor.Verdict, error) {
	return supervisor.RunAdapter(ctx, supervisor.AdapterOptions{
		Provider:     opts.Supervisor,
		WorkDir:      workDir,
		Timeout:      time.Duration(evidence.Task.ExecutionPolicy.TimeoutMS) * time.Millisecond,
		IdleTimeout:  opts.IdleTimeout,
		ArtifactDir:  tryDir,
		ClaudeBin:    opts.ClaudeBin,
		CodexBin:     opts.CodexBin,
		GLMAuthToken: opts.GLMAuthToken,
	}, evidence)
}

func runSupervisorLoop(ctx, shutdownCtx context.Context, opts Options, runningPath string, loaded *task.Task, prepared workspace.Prepared, profiles profile.Bundle, runDir, runID string) error {
	fmt.Fprintf(os.Stderr, "galley: task %s running in %s (run_id=%s)\n", loaded.ID, prepared.CWD, runID)
	// processClaimedTask resolved profiles before workspace creation and
	// already wrote runs/<run-id>/profiles.json. Threading the resolved
	// bundle through this function keeps the supervisor loop from
	// double-loading the environment profile.
	effectiveOpts := effectiveOptionsForProfiles(opts, profiles)
	// Persist the resolved supervisor and its source as run evidence.
	// Reviewers can then tell whether the per-task supervisor came from the
	// repository environment profile, daemon CLI startup options, daemon.yaml,
	// or the built-in default without re-deriving the resolution from logs.
	if err := writeSupervisorEvidence(runDir, effectiveOpts); err != nil {
		fmt.Fprintf(os.Stderr, "galley: write supervisor evidence for run %s failed: %v\n", runID, err)
	}
	preflightResult, err := skeletonpreflight.LoadResult(runDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "galley: could not load preflight result for run %s: %v\n", runID, err)
	}
	// Setup result is loaded from runs/<run-id>/setup_result.json which the
	// setup executor preflight wrote before this loop. It is appended to the
	// implementation work order so the executor sees the readiness facts and
	// threaded into supervisor evidence so reviewers can verify them.
	setupResultEvidence, setupUpdateEvidence := setuppreflight.LoadRunEvidence(runDir, runID)
	promptTask := executionTask(*loaded, prepared.CWD)
	if preflightResult != nil {
		// Runtime obligations below are the source of truth after preflight.
		// Suppress static task-output rendering here to avoid duplicate
		// sections after processClaimedTask writes generated outputs back to
		// the running task file for auditability.
		if promptTask.Preflight != nil && promptTask.Preflight.AcceptanceSkeleton != nil {
			cfgCopy := *promptTask.Preflight.AcceptanceSkeleton
			cfgCopy.Outputs = nil
			preflightCopy := *promptTask.Preflight
			preflightCopy.AcceptanceSkeleton = &cfgCopy
			promptTask.Preflight = &preflightCopy
		}
	}
	prompt := task.RenderWorkOrderWithProfiles(promptTask, profiles)
	if preflightResult != nil {
		prompt = skeletonpreflight.AppendObligations(prompt, preflightResult)
	}
	if setupResultEvidence != nil {
		prompt = setuppreflight.AppendReadinessObligations(prompt, setupResultEvidence, setupUpdateEvidence)
	}
	budget := attemptBudget(loaded.ExecutionPolicy.LoopBudget)
	consecutiveNoDiff := 0
	for attempt := 1; budget < 0 || attempt <= budget; attempt++ {
		review, err := runOneSupervisorAttempt(ctx, supervisorAttemptRequest{
			Opts:     effectiveOpts,
			Loaded:   loaded,
			Prepared: prepared,
			Profiles: profiles,
			RunDir:   runDir,
			RunID:    runID,
			Attempt:  attempt,
			Budget:   budget,
			Prompt:   prompt,
		})
		if err != nil {
			// When an exhausted supervisor idle timeout fails the task,
			// emit one concise line in the existing Galley log tone so the
			// daemon log distinguishes it from a task total-timeout expiry.
			if idle, ok := asSupervisorIdleTimeout(err); ok {
				fmt.Fprintln(os.Stderr, idle.logLine(loaded.ID))
			}
			return taskstate.FailMove(opts.Root, runningPath, loaded, err)
		}
		mergeAttemptEvidence(loaded, review.Outcome, runID, prepared.CWD, review.AttemptDir)
		// Progress detection. The dirty-diff
		// signal alone over-counts: preflight materialized skeleton files in
		// the worktree before the first attempt, so every attempt would
		// otherwise see those same skeleton files as a "diff" and never
		// escalate. hasNonSkeletonProgress excludes baseline-matching
		// skeleton files from the dirty set so changed skeletons (or any
		// non-skeleton change) count as progress, while unchanged skeletons
		// across repeated attempts let the existing no-diff invariant fire.
		nonSkeletonProgress := false
		if review.Outcome.DiffErr == nil {
			progress, _ := hasNonSkeletonProgress(review.Outcome.DiffSnapshot, prepared.CWD, preflightResult)
			nonSkeletonProgress = progress
		}
		if !nonSkeletonProgress {
			consecutiveNoDiff++
		} else {
			consecutiveNoDiff = 0
		}
		fmt.Fprintf(os.Stderr, "galley: task %s attempt %d verdict=%s summary=%s\n", loaded.ID, attempt, review.Verdict.Status, review.Verdict.Summary)
		loaded.Attempts[len(loaded.Attempts)-1].SupervisorVerdict = review.Verdict.Status
		loaded.Attempts[len(loaded.Attempts)-1].Summary = fmt.Sprintf("%s; run_id=%s; attempt=%d; workspace=%s", review.Verdict.Summary, runID, attempt, prepared.CWD)
		if err := task.Save(runningPath, *loaded); err != nil {
			return taskstate.FailMove(opts.Root, runningPath, loaded, err)
		}
		nextPrompt, done, err := applySupervisorVerdict(ctx, shutdownCtx, verdictApplication{
			Opts:              effectiveOpts,
			RunningPath:       runningPath,
			Loaded:            loaded,
			Prepared:          prepared,
			RunDir:            runDir,
			Attempt:           attempt,
			ConsecutiveNoDiff: consecutiveNoDiff,
			Verdict:           review.Verdict,
		})
		if err != nil || done {
			return err
		}
		if nextPrompt != "" {
			prompt = nextPrompt
			continue
		}
	}
	loaded.Status = "needs_supervisor_review"
	fmt.Fprintf(os.Stderr, "galley: task %s exhausted attempts; needs supervisor review\n", loaded.ID)
	return taskstate.Move(opts.Root, runningPath, "failed", loaded)
}

type attemptReview struct {
	AttemptDir string
	Outcome    attemptOutcome
	Verdict    supervisor.Verdict
}

type supervisorAttemptRequest struct {
	Opts     Options
	Loaded   *task.Task
	Prepared workspace.Prepared
	Profiles profile.Bundle
	RunDir   string
	RunID    string
	Attempt  int
	Budget   int
	Prompt   string
}

func runOneSupervisorAttempt(ctx context.Context, req supervisorAttemptRequest) (attemptReview, error) {
	fmt.Fprintf(os.Stderr, "galley: task %s attempt %d/%s starting\n", req.Loaded.ID, req.Attempt, req.Loaded.ExecutionPolicy.LoopBudget.String())
	attemptDir := filepath.Join(req.RunDir, fmt.Sprintf("attempt-%d", req.Attempt))
	if err := os.MkdirAll(attemptDir, 0o700); err != nil {
		appendFailureAttempt(req.Loaded, "attempt_setup", "attempt_setup_failed", err, req.RunDir)
		return attemptReview{}, fmt.Errorf("create attempt dir %s: %w", attemptDir, err)
	}
	effectiveTask := executionTask(*req.Loaded, req.Prepared.CWD)
	effectiveTaskPath := filepath.Join(attemptDir, "task.effective.yaml")
	if err := task.Save(effectiveTaskPath, effectiveTask); err != nil {
		appendFailureAttempt(req.Loaded, "attempt_setup", "attempt_setup_failed", err, attemptDir)
		return attemptReview{}, err
	}
	// Load the runtime preflight result before the executor attempt so the
	// executor result and supervisor evidence share the same generated
	// skeleton bindings.
	preflightOutputs, err := skeletonpreflight.LoadResult(req.RunDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "galley: task %s attempt %d could not load preflight result: %v\n", req.Loaded.ID, req.Attempt, err)
	}
	outcome, err := runExecutorAttempt(ctx, req.Opts, effectiveTask, req.Profiles, req.Prepared.CWD, req.Prepared.BaseSHA, attemptDir, req.Prompt, effectiveTaskPath, preflightOutputs)
	if err != nil {
		// A review-time staging failure is recorded under a distinct phase
		// and kind so the failed task surfaces the staging-related error to
		// the supervisor and operators instead of mis-classifying it as an
		// executor failure.
		if _, ok := asReviewStagingError(err); ok {
			appendFailureAttempt(req.Loaded, "review_staging", "review_staging_failed", err, attemptDir)
			return attemptReview{}, err
		}
		appendFailureAttempt(req.Loaded, "executor", classifyFailureKind("executor_failed", err), err, attemptDir)
		return attemptReview{}, err
	}
	setupResultEvidence, setupUpdateEvidence := setuppreflight.LoadRunEvidence(req.RunDir, req.RunID)
	evidence := supervisor.Evidence{
		Task:                   *req.Loaded,
		Profiles:               req.Profiles,
		Claude:                 outcome.ClaudeResult,
		ParseError:             outcome.ParseErr,
		RunError:               outcome.RunErr,
		DiffDirty:              outcome.DiffDirty,
		Diff:                   outcome.Diff,
		DiffError:              outcome.DiffErr,
		Attempt:                req.Attempt,
		AttemptsLeft:           attemptsLeft(req.Budget, req.Attempt),
		PreflightResult:        preflightOutputs,
		SetupResult:            setupResultEvidence,
		SetupEnvironmentUpdate: setupUpdateEvidence,
	}
	verdict, err := evaluateSupervisorWithRetry(ctx, req.Opts, evidence, attemptDir, req.Prepared.CWD)
	if err != nil {
		appendSupervisorFailureAttempt(req.Loaded, outcome, err, attemptDir)
		return attemptReview{}, err
	}
	if err := writeJSON(runartifact.Path(attemptDir, runartifact.SupervisorVerdictFilename), verdict); err != nil {
		appendFailureAttempt(req.Loaded, "run_evidence", "run_evidence_failed", err, attemptDir)
		return attemptReview{}, err
	}
	return attemptReview{AttemptDir: attemptDir, Outcome: outcome, Verdict: verdict}, nil
}

type verdictApplication struct {
	Opts              Options
	RunningPath       string
	Loaded            *task.Task
	Prepared          workspace.Prepared
	RunDir            string
	Attempt           int
	ConsecutiveNoDiff int
	Verdict           supervisor.Verdict
}

func applySupervisorVerdict(ctx, shutdownCtx context.Context, req verdictApplication) (string, bool, error) {
	if shutdownCtx.Err() != nil && req.Verdict.Status == "needs_revision" {
		fmt.Fprintf(os.Stderr, "galley: task %s stopped after attempt %d due to shutdown\n", req.Loaded.ID, req.Attempt)
		return "", true, degradeToSupervisorReview(req, "shutdown", "Shutdown was requested after an attempt that needs revision; Galley did not start another retry attempt.", "Review the run evidence and requeue the task when ready.")
	}
	if shutdownCtx.Err() != nil && req.Verdict.Status == "accepted" && req.Opts.CommitOnAccept {
		fmt.Fprintf(os.Stderr, "galley: task %s accepted during shutdown; skipped finalization\n", req.Loaded.ID)
		return "", true, degradeToSupervisorReview(req, "shutdown-finalize", "Shutdown was requested before accepted work was finalized; Galley skipped commit, push, and PR creation to avoid an interrupted external side effect.", "Inspect the accepted diff and requeue or finalize manually when ready.")
	}
	if req.Verdict.Status == "needs_revision" && req.ConsecutiveNoDiff >= progressNoDiffThreshold {
		fmt.Fprintf(os.Stderr, "galley: task %s stopped by progress invariant: consecutive no-diff attempts\n", req.Loaded.ID)
		return "", true, degradeToSupervisorReview(req, "progress", "Two consecutive executor attempts produced no git diff.", "A supervisor should inspect the task, work order, and executor logs before requeueing.")
	}

	switch req.Verdict.Status {
	case "accepted":
		// Daemon-side acceptance gate. When required skeleton
		// coverage or required-check evidence is missing or failed, downgrade
		// the accepted verdict to needs_supervisor_review with a user-visible
		// reason. There is no waiver mechanism.
		if reason, ok := evaluateAcceptanceGate(req.Loaded, req.RunDir); !ok {
			fmt.Fprintf(os.Stderr, "galley: task %s accepted-verdict downgraded by acceptance gate: %s\n", req.Loaded.ID, reason)
			return "", true, degradeToSupervisorReview(req, "acceptance-gate", "Accepted verdict downgraded by acceptance skeleton gate: "+reason, "Inspect preflight_result.json and required verification evidence before re-finalizing.")
		}
		return "", true, acceptSupervisorVerdict(ctx, req.Opts, req.RunningPath, req.Loaded, req.Prepared, req.RunDir, req.Verdict)
	case "needs_revision":
		return req.Verdict.NextWorkOrder, false, nil
	case "hard_stop":
		req.Loaded.Status = "failed"
		return "", true, taskstate.Move(req.Opts.Root, req.RunningPath, "failed", req.Loaded)
	case "needs_supervisor_review":
		req.Loaded.Status = "needs_supervisor_review"
		return "", true, taskstate.Move(req.Opts.Root, req.RunningPath, "failed", req.Loaded)
	default:
		fmt.Fprintf(os.Stderr, "galley: task %s unknown supervisor verdict=%q\n", req.Loaded.ID, req.Verdict.Status)
		return "", true, degradeToSupervisorReview(req, "supervisor-verdict", fmt.Sprintf("Supervisor returned unknown verdict status %q.", req.Verdict.Status), "Inspect supervisor_verdict.json and rerun after correcting the supervisor output.")
	}
}

func degradeToSupervisorReview(req verdictApplication, riskPrefix, detail, mitigation string) error {
	req.Loaded.Status = "needs_supervisor_review"
	appendRisk(req.Loaded, riskPrefix, "partial_verification", detail, mitigation, true)
	return taskstate.Move(req.Opts.Root, req.RunningPath, "failed", req.Loaded)
}

func acceptSupervisorVerdict(ctx context.Context, opts Options, runningPath string, loaded *task.Task, prepared workspace.Prepared, runDir string, verdict supervisor.Verdict) error {
	markRevisionRequestsAddressed(loaded, verdict.Summary)
	applyAcceptedAcceptanceCriteria(loaded, verdict)
	mergeDiscussionItems(loaded, verdict)
	if opts.CommitOnAccept {
		fmt.Fprintf(os.Stderr, "galley: task %s accepted; finalizing commit/pr\n", loaded.ID)
		if err := finalizeAcceptedChange(ctx, opts, loaded, prepared.CWD, prepared.BaseSHA, runDir); err != nil {
			loaded.Status = "needs_supervisor_review"
			appendRisk(loaded, "finalize", "partial_verification", err.Error(), "The executor diff and run evidence were stored; a supervisor should inspect and finish commit or PR creation.", true)
			return taskstate.FailMove(opts.Root, runningPath, loaded, err)
		}
	} else if err := inputfiles.CleanupNonCommitted(prepared.CWD, loaded.Files); err != nil {
		loaded.Status = "needs_supervisor_review"
		appendRisk(loaded, "input-file-cleanup", "partial_verification", err.Error(), "Remove non-committed task input files from the execution workspace before archiving or reusing it.", true)
		return taskstate.FailMove(opts.Root, runningPath, loaded, err)
	}
	loaded.Status = "accepted"
	if opts.OpenPR {
		loaded.Status = "pr_opened"
	}
	fmt.Fprintf(os.Stderr, "galley: task %s completed with status=%s\n", loaded.ID, loaded.Status)
	return taskstate.Move(opts.Root, runningPath, "done", loaded)
}

func executionTask(loaded task.Task, workDir string) task.Task {
	loaded.Scope.CWD = workDir
	return loaded
}

// evaluateSupervisorWithRetry runs the supervisor evaluation and, when the
// supervisor subprocess fails because of idle timeout, total timeout, or a
// forced subprocess kill, retries the same evaluation up to
// supervisorRetryBudget additional times within the same executor attempt.
// Each try writes its artifacts under a distinct supervisor-try-N subdirectory
// so retry evidence remains inspectable.
func evaluateSupervisorWithRetry(ctx context.Context, opts Options, evidence supervisor.Evidence, attemptDir, workDir string) (supervisor.Verdict, error) {
	var lastErr error
	idleTimeoutFailures := 0
	for try := 1; try <= supervisorTotalAttempts; try++ {
		tryDir := filepath.Join(attemptDir, fmt.Sprintf("supervisor-try-%d", try))
		if err := os.MkdirAll(tryDir, 0o700); err != nil {
			return supervisor.Verdict{}, fmt.Errorf("create supervisor try dir %s: %w", tryDir, err)
		}
		verdict, err := supervisorRunner(ctx, opts, evidence, tryDir, workDir)
		if err == nil {
			// Per-retry verdict.
			if writeErr := writeJSON(runartifact.Path(tryDir, runartifact.SupervisorVerdictFilename), verdict); writeErr != nil {
				return supervisor.Verdict{}, writeErr
			}
			// Preserve the top-level evidence path that downstream tools and
			// existing tests rely on.
			if writeErr := writeJSON(runartifact.Path(attemptDir, runartifact.ModelSupervisorVerdictFilename), verdict); writeErr != nil {
				return supervisor.Verdict{}, writeErr
			}
			if try > 1 {
				fmt.Fprintf(os.Stderr, "galley: supervisor evaluation recovered on try %d after %d transient failure(s)\n", try, try-1)
			}
			return verdict, nil
		}
		// Record the error JSON for this try so operators can inspect every
		// retry, including the kind classification.
		kind := classifyFailureKind("supervisor_failed", err)
		_ = writeJSON(runartifact.Path(tryDir, runartifact.SupervisorErrorFilename), map[string]any{
			"try":   try,
			"kind":  kind,
			"error": err.Error(),
		})
		if ctxErr := ctx.Err(); ctxErr != nil {
			// Shutdown or task-level cancel: do not consume retry budget.
			return supervisor.Verdict{}, err
		}
		if !isSupervisorStallError(err) {
			return supervisor.Verdict{}, err
		}
		if isIdleTimeoutError(err) {
			idleTimeoutFailures++
		}
		lastErr = err
		fmt.Fprintf(os.Stderr, "galley: supervisor try %d/%d failed (%s); %d retry budget remaining\n", try, supervisorTotalAttempts, kind, supervisorTotalAttempts-try)
	}
	// An exhausted idle timeout is the watchdog-recoverable failure mode the
	// user-facing reporting must distinguish from a task total-timeout expiry.
	// Wrap it in a typed error so appendSupervisorFailureAttempt and
	// runSupervisorLoop can stamp the distinct supervisor_idle_timeout kind and
	// log line. Total timeout and forced-kill exhaustion keep the existing
	// generic wrapped error. Mixed stall causes also keep the generic path
	// because supervisor_idle_timeout specifically means every try was killed
	// by the idle-output watchdog.
	if idleTimeoutFailures == supervisorTotalAttempts {
		return supervisor.Verdict{}, &supervisorIdleTimeoutError{
			Supervisor:  opts.Supervisor,
			IdleTimeout: opts.IdleTimeout,
			Tries:       supervisorTotalAttempts,
			MaxTries:    supervisorTotalAttempts,
			Err:         lastErr,
		}
	}
	return supervisor.Verdict{}, fmt.Errorf("supervisor evaluation failed after %d tries: %w", supervisorTotalAttempts, lastErr)
}

// isSupervisorStallError reports whether the supervisor subprocess exited
// because of idle timeout, total timeout, or a forced subprocess kill - the
// only failure modes that trigger an in-attempt supervisor retry. Other
// errors (e.g. a malformed verdict or an unrecognized provider) are treated
// as permanent so they surface immediately instead of consuming retries.
func isSupervisorStallError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	if errors.Is(err, runner.ErrIdleTimeout) ||
		errors.Is(err, runner.ErrTimeout) ||
		errors.Is(err, runner.ErrKilled) {
		return true
	}
	return false
}

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

func mergeDiscussionItems(loaded *task.Task, verdict supervisor.Verdict) {
	for _, item := range verdict.DiscussionItems {
		loaded.DiscussionItems = append(loaded.DiscussionItems, task.DiscussionItem{
			ID:                    fmt.Sprintf("discussion-%d", len(loaded.DiscussionItems)+1),
			Topic:                 item.Topic,
			Summary:               item.Summary,
			RequiresHumanDecision: item.RequiresHumanDecision,
		})
	}
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

// prepareClaudeExecutorPlan builds the Claude executor command plan and the
// stdout/stderr capture paths used by runExecutorAttempt.
func prepareClaudeExecutorPlan(opts Options, loaded task.Task, workDir, prompt, attemptDir string) (runner.Command, string, string, error) {
	claudeOpts := runner.FromTask(loaded)
	claudeOpts.Bin = opts.ClaudeBin
	claudeOpts.WorkDir = workDir
	claudeOpts.SystemPromptFile = opts.SystemPromptFile
	claudeOpts.JSONSchemaFile = opts.JSONSchemaFile
	claudeOpts.AttemptDir = attemptDir
	claudeOpts.Prompt = prompt
	if !opts.DisableClaudeGuard {
		guardDir := opts.ClaudeGuardPluginDir
		if guardDir == "" {
			guardDir = filepath.Join(opts.Root, "runtime", "claude-guard-plugin")
		}
		guardDir, err := claudeguard.Ensure(guardDir)
		if err != nil {
			return runner.Command{}, "", "", err
		}
		guardDir, err = filepath.Abs(guardDir)
		if err != nil {
			return runner.Command{}, "", "", fmt.Errorf("resolve Claude guard plugin dir: %w", err)
		}
		claudeOpts.PluginDirs = append(claudeOpts.PluginDirs, guardDir)
	}
	plan, err := runner.ClaudeCommandPlan(claudeOpts)
	if err != nil {
		return runner.Command{}, "", "", err
	}
	return plan, filepath.Join(attemptDir, "claude.stdout.jsonl"), filepath.Join(attemptDir, "claude.stderr.log"), nil
}

// prepareGLMExecutorPlan builds the GLM implementation executor command plan by
// reusing the Claude launch path and redirecting the Claude binary at GLM's
// endpoint via runner.RedirectClaudeToGLM. It fails fast with an actionable,
// secret-free error when executor.cli "glm" was selected without a configured
// glm_api_key. The stdout/stderr paths reuse the claude.* names because GLM
// produces the same stream-json output. Setup and acceptance-skeleton executors
// apply the identical redirect in their own packages, so all executor roles
// honor executor.cli "glm".
func prepareGLMExecutorPlan(opts Options, loaded task.Task, workDir, prompt, attemptDir string) (runner.Command, string, string, error) {
	token, err := runner.ResolveGLMToken(opts.GLMAuthToken)
	if err != nil {
		return runner.Command{}, "", "", err
	}
	plan, stdoutPath, stderrPath, err := prepareClaudeExecutorPlan(opts, loaded, workDir, prompt, attemptDir)
	if err != nil {
		return runner.Command{}, "", "", err
	}
	runner.RedirectClaudeToGLM(&plan, token)
	return plan, stdoutPath, stderrPath, nil
}

// prepareCodexExecutorPlan builds the Codex executor command plan and the
// stdout/stderr capture paths used by runExecutorAttempt. The stdout file is
// named codex.stdout.jsonl to mirror the claude.stdout.jsonl convention.
//
// AttemptDir is threaded through to the runner so JSONSchemaFile/JSONSchema is
// mapped onto a real `codex exec --output-schema <file>` path and the daemon
// also requests `--output-last-message <file>` under the same attempt
// directory. The Codex CLI writes the final assistant message to that file,
// which resolveCodexResult then parses to preserve completed,
// completed_with_risks, and hard_stop executor results for supervisor handoff.
func prepareCodexExecutorPlan(opts Options, loaded task.Task, workDir, prompt, attemptDir string) (runner.Command, string, string, error) {
	codexOpts := runner.CodexFromTask(loaded)
	codexOpts.Bin = opts.CodexBin
	codexOpts.WorkDir = workDir
	codexOpts.SystemPromptFile = opts.SystemPromptFile
	codexOpts.JSONSchemaFile = opts.JSONSchemaFile
	codexOpts.AttemptDir = attemptDir
	codexOpts.Prompt = prompt
	plan, err := runner.CodexCommandPlan(codexOpts)
	if err != nil {
		return runner.Command{}, "", "", err
	}
	return plan, filepath.Join(attemptDir, "codex.stdout.jsonl"), filepath.Join(attemptDir, "codex.stderr.log"), nil
}

func attemptBudget(b task.LoopBudget) int {
	if b.Set && b.Count == 0 {
		return -1
	}
	if b.Set {
		return b.Count
	}
	return task.DefaultLoopBudget
}

func attemptsLeft(budget, attempt int) int {
	if budget < 0 {
		return 1
	}
	return budget - attempt
}

// writeSupervisorEvidence persists the resolved supervisor and its source for
// a run so reviewers can verify which precedence layer (repository
// environment profile, CLI startup flag, daemon.yaml, or built-in default)
// determined the supervisor adapter Galley used. The function is
// extracted from runSupervisorLoop so it can be unit-tested without driving a
// full task through the daemon loop.
func writeSupervisorEvidence(runDir string, effectiveOpts Options) error {
	return writeJSON(filepath.Join(runDir, "supervisor.json"), map[string]string{
		"resolved": effectiveOpts.Supervisor,
		"source":   effectiveOpts.SupervisorSource,
	})
}
