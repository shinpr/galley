package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/shinpr/galley/internal/inputfiles"
	setuppreflight "github.com/shinpr/galley/internal/preflight/setup"
	skeletonpreflight "github.com/shinpr/galley/internal/preflight/skeleton"
	"github.com/shinpr/galley/internal/profile"
	"github.com/shinpr/galley/internal/runartifact"
	"github.com/shinpr/galley/internal/runner"
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
	// processClaimedTask resolved profiles and effective task options once
	// before workspace creation. The opts received here are that immutable
	// claimed-task resolution; maintenance paths resolve independently.
	effectiveOpts := opts
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
			return taskstate.FailMoveToStatus(opts.Root, runningPath, loaded, err)
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
			return taskstate.FailMoveToStatus(opts.Root, runningPath, loaded, err)
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
	return taskstate.MoveToStatus(opts.Root, runningPath, loaded)
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
		ExecutorResult:         outcome.ExecutorResult,
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
		return "", true, taskstate.MoveToStatus(req.Opts.Root, req.RunningPath, req.Loaded)
	case "needs_supervisor_review":
		req.Loaded.Status = "needs_supervisor_review"
		return "", true, taskstate.MoveToStatus(req.Opts.Root, req.RunningPath, req.Loaded)
	default:
		fmt.Fprintf(os.Stderr, "galley: task %s unknown supervisor verdict=%q\n", req.Loaded.ID, req.Verdict.Status)
		return "", true, degradeToSupervisorReview(req, "supervisor-verdict", fmt.Sprintf("Supervisor returned unknown verdict status %q.", req.Verdict.Status), "Inspect supervisor_verdict.json and rerun after correcting the supervisor output.")
	}
}

func degradeToSupervisorReview(req verdictApplication, riskPrefix, detail, mitigation string) error {
	req.Loaded.Status = "needs_supervisor_review"
	appendRisk(req.Loaded, riskPrefix, "partial_verification", detail, mitigation, true)
	return taskstate.MoveToStatus(req.Opts.Root, req.RunningPath, req.Loaded)
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
			return taskstate.FailMoveToStatus(opts.Root, runningPath, loaded, err)
		}
	} else if err := inputfiles.CleanupNonCommitted(prepared.CWD, loaded.Files); err != nil {
		loaded.Status = "needs_supervisor_review"
		appendRisk(loaded, "input-file-cleanup", "partial_verification", err.Error(), "Remove non-committed task input files from the execution workspace before archiving or reusing it.", true)
		return taskstate.FailMoveToStatus(opts.Root, runningPath, loaded, err)
	}
	loaded.Status = "accepted"
	if opts.OpenPR {
		loaded.Status = "pr_opened"
	}
	fmt.Fprintf(os.Stderr, "galley: task %s completed with status=%s\n", loaded.ID, loaded.Status)
	return taskstate.MoveToStatus(opts.Root, runningPath, loaded)
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
