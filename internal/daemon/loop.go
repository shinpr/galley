package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shinpr/galley/internal/inputfiles"
	setuppreflight "github.com/shinpr/galley/internal/preflight/setup"
	skeletonpreflight "github.com/shinpr/galley/internal/preflight/skeleton"
	"github.com/shinpr/galley/internal/profile"
	"github.com/shinpr/galley/internal/runartifact"
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

// supervisorRunner runs one supervisor evaluation against evidence using
// artifactDir as the artifact directory. A package-level function variable
// keeps the handoff testable without spawning a real provider process.
var supervisorRunner = defaultSupervisorRunner

func defaultSupervisorRunner(ctx context.Context, opts Options, evidence supervisor.Evidence, tryDir, workDir string) (supervisor.Verdict, error) {
	return supervisor.RunAdapter(ctx, supervisor.AdapterOptions{
		Provider:     opts.Supervisor,
		Model:        opts.SupervisorModel,
		Effort:       opts.SupervisorEffort,
		WorkDir:      workDir,
		Timeout:      time.Duration(evidence.Task.ExecutionPolicy.TimeoutMS) * time.Millisecond,
		IdleTimeout:  opts.IdleTimeout,
		ArtifactDir:  tryDir,
		ClaudeBin:    opts.ClaudeBin,
		CodexBin:     opts.CodexBin,
		GrokBin:      opts.GrokBin,
		GLMAuthToken: opts.GLMAuthToken,
		KimiAPIKey:   opts.KimiAPIKey,
	}, evidence)
}

func runSupervisorLoop(ctx, shutdownCtx context.Context, opts Options, runningPath string, loaded *task.Task, prepared claimedWorkspace, profiles profile.Bundle, runDir, runID string, effectiveExecutor task.Executor) error {
	fmt.Fprintf(os.Stderr, "galley: task %s running in %s (run_id=%s)\n", loaded.ID, prepared.CWD, runID)
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
	supervisor.ReconcileReviewProgressWithContext(loaded, profiles, prepared.ReviewContractContext)
	promptTask := executionTask(*loaded, prepared.CWD, effectiveExecutor)
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
	budget := attemptBudget(loaded.ExecutionPolicy.LoopBudget)
	consecutiveNoDiff := 0
	revision := supervisorRevisionFromTask(promptTask)
	for attempt := 1; budget < 0 || attempt <= budget; attempt++ {
		attemptPromptTask := revision.applyToTask(promptTask)
		prompt := task.RenderWorkOrderWithProfiles(attemptPromptTask, profiles)
		if preflightResult != nil {
			prompt = skeletonpreflight.AppendObligations(prompt, preflightResult)
		}
		if setupResultEvidence != nil {
			prompt = setuppreflight.AppendReadinessObligations(prompt, setupResultEvidence, setupUpdateEvidence)
		}
		effectiveTask := revision.applyToTask(executionTask(*loaded, prepared.CWD, effectiveExecutor))
		review, err := runOneSupervisorAttempt(ctx, supervisorAttemptRequest{
			Opts:          effectiveOpts,
			Loaded:        loaded,
			Prepared:      prepared,
			Profiles:      profiles,
			RunDir:        runDir,
			RunID:         runID,
			Attempt:       attempt,
			Budget:        budget,
			Prompt:        prompt,
			EffectiveTask: effectiveTask,
		})
		if err != nil {
			// When a supervisor idle timeout fails the task,
			// emit one concise line in the existing Galley log tone so the
			// daemon log distinguishes it from a task total-timeout expiry.
			if idle, ok := asSupervisorIdleTimeout(err); ok {
				fmt.Fprintln(os.Stderr, idle.logLine(loaded.ID))
			}
			if interruption, ok := asExecutorInterruptionError(err); ok {
				fmt.Fprintf(os.Stderr, "galley: task %s executor interrupted (%s) before Supervisor review; preserving worktree for requeue\n", loaded.ID, interruption.terminal.Reason)
			}
			return taskstate.FailMoveToStatus(opts.Root, runningPath, loaded, err)
		}
		mergeAttemptEvidence(loaded, review.Outcome, runID, prepared.CWD, review.AttemptDir)
		supervisor.ApplyReviewProgressWithContext(loaded, profiles, prepared.ReviewContractContext, review.Verdict)
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
		if review.Verdict.Status == "needs_revision" {
			revision = nextSupervisorRevision(len(loaded.Attempts), review.Verdict)
			*loaded = revision.applyToTask(*loaded)
		}
		if err := task.Save(runningPath, *loaded); err != nil {
			return taskstate.FailMoveToStatus(opts.Root, runningPath, loaded, err)
		}
		done, err := applySupervisorVerdict(ctx, shutdownCtx, verdictApplication{
			Opts:              effectiveOpts,
			RunningPath:       runningPath,
			Loaded:            loaded,
			Prepared:          prepared.Prepared,
			Profiles:          profiles,
			RunDir:            runDir,
			Attempt:           attempt,
			ConsecutiveNoDiff: consecutiveNoDiff,
			Verdict:           review.Verdict,
		})
		if err != nil || done {
			return err
		}
	}
	*loaded = revision.applyToTask(*loaded)
	loaded.Status = "failed"
	fmt.Fprintf(os.Stderr, "galley: task %s exhausted attempts\n", loaded.ID)
	return taskstate.MoveToStatus(opts.Root, runningPath, loaded)
}

type attemptReview struct {
	AttemptDir string
	Outcome    attemptOutcome
	Verdict    supervisor.Verdict
}

type supervisorAttemptRequest struct {
	Opts          Options
	Loaded        *task.Task
	Prepared      claimedWorkspace
	Profiles      profile.Bundle
	RunDir        string
	RunID         string
	Attempt       int
	Budget        int
	Prompt        string
	EffectiveTask task.Task
}

func runOneSupervisorAttempt(ctx context.Context, req supervisorAttemptRequest) (attemptReview, error) {
	fmt.Fprintf(os.Stderr, "galley: task %s attempt %d/%s starting\n", req.Loaded.ID, req.Attempt, req.Loaded.ExecutionPolicy.LoopBudget.String())
	attemptDir := filepath.Join(req.RunDir, fmt.Sprintf("attempt-%d", req.Attempt))
	if err := os.MkdirAll(attemptDir, 0o700); err != nil {
		appendFailureAttempt(req.Loaded, "attempt_setup", "attempt_setup_failed", err, req.RunDir)
		return attemptReview{}, fmt.Errorf("create attempt dir %s: %w", attemptDir, err)
	}
	effectiveTask := req.EffectiveTask
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
	// A provider or runtime interruption never reaches Supervisor and never
	// starts another executor attempt in this run. The attempt evidence and the
	// dirty worktree are already captured, so publishing to tasks/failed
	// preserves the partial work for `galley task requeue`.
	if outcome.Terminal.Interrupted() {
		appendExecutorInterruptionAttempt(req.Loaded, outcome, attemptDir)
		return attemptReview{}, &executorInterruptionError{terminal: outcome.Terminal}
	}
	setupResultEvidence, setupUpdateEvidence := setuppreflight.LoadRunEvidence(req.RunDir, req.RunID)
	evidence := supervisor.Evidence{
		Task:                   effectiveTask,
		Profiles:               req.Profiles,
		ReviewContractContext:  req.Prepared.ReviewContractContext,
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
	verdict, err := evaluateSupervisor(ctx, req.Opts, evidence, attemptDir, req.Prepared.CWD)
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
	Profiles          profile.Bundle
	RunDir            string
	Attempt           int
	ConsecutiveNoDiff int
	Verdict           supervisor.Verdict
}

func applySupervisorVerdict(ctx, shutdownCtx context.Context, req verdictApplication) (bool, error) {
	if shutdownCtx.Err() != nil && req.Verdict.Status == "needs_revision" {
		fmt.Fprintf(os.Stderr, "galley: task %s stopped after attempt %d due to shutdown\n", req.Loaded.ID, req.Attempt)
		return true, failVerdictApplication(req, "shutdown", "Shutdown was requested after an attempt that needs revision; Galley did not start another retry attempt.", "Review the run evidence and requeue the task when ready.")
	}
	if shutdownCtx.Err() != nil && req.Verdict.Status == "accepted" && req.Opts.CommitOnAccept {
		fmt.Fprintf(os.Stderr, "galley: task %s accepted during shutdown; skipped finalization\n", req.Loaded.ID)
		return true, failVerdictApplication(req, "shutdown-finalize", "Shutdown was requested before accepted work was finalized; Galley skipped commit, push, and PR creation to avoid an interrupted external side effect.", "Inspect the accepted diff and requeue or finalize manually when ready.")
	}
	if req.Verdict.Status == "needs_revision" && req.ConsecutiveNoDiff >= progressNoDiffThreshold {
		fmt.Fprintf(os.Stderr, "galley: task %s stopped by progress invariant: consecutive no-diff attempts\n", req.Loaded.ID)
		return true, failVerdictApplication(req, "progress", "Two consecutive executor attempts produced no git diff.", "Inspect the task, work order, and executor logs before requeueing.")
	}

	switch req.Verdict.Status {
	case "accepted":
		// Required skeleton coverage is a task contract that cannot be waived
		// by the supervisor.
		if reason, ok := evaluateAcceptanceGate(req.Loaded, req.RunDir); !ok {
			fmt.Fprintf(os.Stderr, "galley: task %s accepted verdict rejected by acceptance gate: %s\n", req.Loaded.ID, reason)
			return true, failVerdictApplication(req, "acceptance-gate", "Accepted verdict rejected by acceptance skeleton gate: "+reason, "Inspect preflight_result.json before re-finalizing.")
		}
		return true, acceptSupervisorVerdict(ctx, req.Opts, req.RunningPath, req.Loaded, req.Prepared, req.Profiles, req.RunDir, req.Verdict)
	case "needs_revision":
		return false, nil
	case "hard_stop":
		req.Loaded.Status = "failed"
		return true, taskstate.MoveToStatus(req.Opts.Root, req.RunningPath, req.Loaded)
	case "needs_supervisor_review":
		req.Loaded.Status = "needs_supervisor_review"
		return true, taskstate.MoveToStatus(req.Opts.Root, req.RunningPath, req.Loaded)
	default:
		fmt.Fprintf(os.Stderr, "galley: task %s unknown supervisor verdict=%q\n", req.Loaded.ID, req.Verdict.Status)
		return true, failVerdictApplication(req, "supervisor-verdict", fmt.Sprintf("Supervisor returned unknown verdict status %q.", req.Verdict.Status), "Inspect supervisor_verdict.json and rerun after correcting the supervisor output.")
	}
}

func failVerdictApplication(req verdictApplication, riskPrefix, detail, mitigation string) error {
	req.Loaded.Status = "failed"
	appendRisk(req.Loaded, riskPrefix, "partial_verification", detail, mitigation, true)
	return taskstate.MoveToStatus(req.Opts.Root, req.RunningPath, req.Loaded)
}

func acceptSupervisorVerdict(ctx context.Context, opts Options, runningPath string, loaded *task.Task, prepared workspace.Prepared, profiles profile.Bundle, runDir string, verdict supervisor.Verdict) error {
	mergeDiscussionItems(loaded, profiles, verdict)
	*loaded = (supervisorRevision{}).applyToTask(*loaded)
	applyAcceptedAcceptanceCriteria(loaded)
	if opts.CommitOnAccept {
		fmt.Fprintf(os.Stderr, "galley: task %s accepted; finalizing commit/pr\n", loaded.ID)
		if err := finalizeAcceptedChange(ctx, opts, loaded, prepared.CWD, prepared.BaseSHA, runDir); err != nil {
			loaded.Status = "failed"
			appendRisk(loaded, "finalize", "partial_verification", err.Error(), "The executor diff and run evidence were stored; a supervisor should inspect and finish commit or PR creation.", true)
			return taskstate.FailMoveToStatus(opts.Root, runningPath, loaded, err)
		}
	} else if err := inputfiles.CleanupNonCommitted(prepared.CWD, loaded.Files); err != nil {
		loaded.Status = "failed"
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

func executionTask(loaded task.Task, workDir string, effectiveExecutor task.Executor) task.Task {
	loaded.Scope.CWD = workDir
	loaded.Executor = effectiveExecutor
	return loaded
}

// evaluateSupervisor runs one supervisor evaluation for an executor attempt.
// A failed supervisor invocation is preserved as evidence and returned to the
// task-state path; Galley does not rerun the same model against unchanged input.
func evaluateSupervisor(ctx context.Context, opts Options, evidence supervisor.Evidence, attemptDir, workDir string) (supervisor.Verdict, error) {
	artifactDir := filepath.Join(attemptDir, "supervisor-try-1")
	if err := os.MkdirAll(artifactDir, 0o700); err != nil {
		return supervisor.Verdict{}, fmt.Errorf("create supervisor artifact dir %s: %w", artifactDir, err)
	}
	verdict, err := opts.daemonDependencies().supervisorRunner(ctx, opts, evidence, artifactDir, workDir)
	if err != nil {
		kind := supervisorFailureKind(err)
		_ = writeJSON(runartifact.Path(artifactDir, runartifact.SupervisorErrorFilename), map[string]any{
			"try":   1,
			"kind":  kind,
			"error": err.Error(),
		})
		if isIdleTimeoutError(err) {
			return supervisor.Verdict{}, &supervisorIdleTimeoutError{
				Supervisor:  opts.Supervisor,
				IdleTimeout: opts.IdleTimeout,
				Err:         err,
			}
		}
		return supervisor.Verdict{}, err
	}
	if err := writeJSON(runartifact.Path(artifactDir, runartifact.SupervisorVerdictFilename), verdict); err != nil {
		return supervisor.Verdict{}, err
	}
	if err := writeJSON(runartifact.Path(attemptDir, runartifact.ModelSupervisorVerdictFilename), verdict); err != nil {
		return supervisor.Verdict{}, err
	}
	return verdict, nil
}

func mergeDiscussionItems(loaded *task.Task, profiles profile.Bundle, verdict supervisor.Verdict) {
	appendDiscussionItem(loaded, "Supervisor summary", verdict.Summary, false)
	for _, finding := range verdict.Findings {
		appendDiscussionItem(loaded, "Supervisor finding", finding, true)
	}
	for _, item := range verdict.DiscussionItems {
		appendDiscussionItem(loaded, "Supervisor discussion", item, false)
	}
	for _, request := range loaded.RevisionRequests {
		if request.Status == "addressed" {
			continue
		}
		appendDiscussionItem(
			loaded,
			"Unverified revision request",
			fmt.Sprintf("revision:%s (%s): %s", request.ID, request.Source, request.Text),
			true,
		)
	}
	if profiles.Quality != nil {
		passed := map[string]bool{}
		if loaded.ReviewProgress != nil {
			for _, id := range loaded.ReviewProgress.Quality {
				passed[id] = true
			}
		}
		var gaps []string
		for _, dimension := range profiles.Quality.ReviewDimensions {
			if !passed[dimension.ID] {
				gaps = append(gaps, dimension.ID)
			}
		}
		if len(gaps) > 0 {
			appendDiscussionItem(loaded, "Quality review gaps", strings.Join(gaps, ", "), true)
		}
	}
}

func appendDiscussionItem(loaded *task.Task, topic, summary string, requiresHumanDecision bool) {
	if strings.TrimSpace(summary) == "" {
		return
	}
	loaded.DiscussionItems = append(loaded.DiscussionItems, task.DiscussionItem{
		ID:                    fmt.Sprintf("discussion-%d", len(loaded.DiscussionItems)+1),
		Topic:                 topic,
		Summary:               summary,
		RequiresHumanDecision: requiresHumanDecision,
	})
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
	modelSource := SupervisorModelSourceCLIDefault
	if effectiveOpts.SupervisorModel != "" {
		modelSource = SupervisorModelSourceRepoProfile
	}
	effortSource := SupervisorEffortSourceCLIDefault
	if effectiveOpts.SupervisorEffort != "" {
		effortSource = SupervisorEffortSourceRepoProfile
	}
	return writeJSON(filepath.Join(runDir, "supervisor.json"), map[string]string{
		"resolved":      effectiveOpts.Supervisor,
		"source":        effectiveOpts.SupervisorSource,
		"model":         effectiveOpts.SupervisorModel,
		"model_source":  modelSource,
		"effort":        effectiveOpts.SupervisorEffort,
		"effort_source": effortSource,
	})
}
