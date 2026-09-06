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

const progressNoDiffThreshold = 2

// supervisorRunRequest is one supervisor invocation.
type supervisorRunRequest struct {
	Opts     Options
	Evidence supervisor.Evidence
	TryDir   string
	WorkDir  string
}

func defaultSupervisorRunner(ctx context.Context, req supervisorRunRequest) (supervisor.Verdict, error) {
	opts, evidence, tryDir, workDir := req.Opts, req.Evidence, req.TryDir, req.WorkDir
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

// withoutStaticSkeletonOutputs drops rendered skeleton outputs from the prompt.
// Runtime obligations own that content after preflight, so keeping both duplicates.
func withoutStaticSkeletonOutputs(promptTask task.Task) task.Task {
	if promptTask.Preflight == nil || promptTask.Preflight.AcceptanceSkeleton == nil {
		return promptTask
	}
	cfgCopy := *promptTask.Preflight.AcceptanceSkeleton
	cfgCopy.Outputs = nil
	preflightCopy := *promptTask.Preflight
	preflightCopy.AcceptanceSkeleton = &cfgCopy
	promptTask.Preflight = &preflightCopy
	return promptTask
}

// logAttemptFailure distinguishes a supervisor idle timeout and an executor
// interruption from a task total-timeout expiry in the daemon log.
func logAttemptFailure(taskID string, err error) {
	if idle, ok := asSupervisorIdleTimeout(err); ok {
		fmt.Fprintln(os.Stderr, idle.logLine(taskID))
	}
	if interruption, ok := asExecutorInterruptionError(err); ok {
		fmt.Fprintf(os.Stderr, "galley: task %s executor interrupted (%s) before Supervisor review; preserving worktree for requeue\n", taskID, interruption.terminal.Reason)
	}
}

// attemptMadeProgress excludes baseline-matching skeletons from the dirty set;
// counting them every attempt would keep the no-diff invariant from firing.
func attemptMadeProgress(outcome attemptOutcome, workDir string, preflightResult *skeletonpreflight.Result) bool {
	if outcome.DiffErr != nil {
		return false
	}
	progress, _ := hasNonSkeletonProgress(outcome.DiffSnapshot, workDir, preflightResult)
	return progress
}

// supervisorLoopRequest is one claimed task's attempt/review loop.
type supervisorLoopRequest struct {
	Opts              Options
	RunningPath       string
	Loaded            *task.Task
	Prepared          claimedWorkspace
	Profiles          profile.Bundle
	RunDir            string
	RunID             string
	EffectiveExecutor task.Executor
}

func runSupervisorLoop(execCtx, daemonCtx context.Context, req supervisorLoopRequest) error {
	opts, runningPath, loaded := req.Opts, req.RunningPath, req.Loaded
	prepared, profiles := req.Prepared, req.Profiles
	runDir, runID, effectiveExecutor := req.RunDir, req.RunID, req.EffectiveExecutor
	fmt.Fprintf(os.Stderr, "galley: task %s running in %s (run_id=%s)\n", loaded.ID, prepared.CWD, runID)
	// Persist the resolved supervisor and its source as run evidence.
	// Reviewers can then tell whether the per-task supervisor came from the
	// repository environment profile, daemon CLI startup options, daemon.yaml,
	// or the built-in default without re-deriving the resolution from logs.
	if err := writeSupervisorEvidence(runDir, opts); err != nil {
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
		promptTask = withoutStaticSkeletonOutputs(promptTask)
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
		review, err := runOneSupervisorAttempt(execCtx, supervisorAttemptRequest{
			Opts:          opts,
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
			logAttemptFailure(loaded.ID, err)
			return taskstate.FailMoveToStatus(opts.Root, runningPath, loaded, err)
		}
		mergeAttemptEvidence(loaded, review.Outcome, attemptEvidencePaths{RunID: runID, WorkDir: prepared.CWD, AttemptDir: review.AttemptDir})
		supervisor.ApplyReviewProgressWithContext(loaded, profiles, prepared.ReviewContractContext, review.Verdict)
		// Progress detection. The dirty-diff
		// signal alone over-counts: preflight materialized skeleton files in
		// the worktree before the first attempt, so every attempt would
		// otherwise see those same skeleton files as a "diff" and never
		// escalate. hasNonSkeletonProgress excludes baseline-matching
		// skeleton files from the dirty set so changed skeletons (or any
		// non-skeleton change) count as progress, while unchanged skeletons
		// across repeated attempts let the existing no-diff invariant fire.
		if attemptMadeProgress(review.Outcome, prepared.CWD, preflightResult) {
			consecutiveNoDiff = 0
		} else {
			consecutiveNoDiff++
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
		done, err := applySupervisorVerdict(execCtx, daemonCtx, verdictApplication{
			Opts:              opts,
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
		// The task file is the source of truth for the next attempt: it carries
		// a finalization failure's new request and drops accepted ones.
		promptTask.RevisionRequests = loaded.RevisionRequests
		revision = supervisorRevisionFromTask(*loaded)
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
	attemptDir := filepath.Join(req.RunDir, runartifact.AttemptDirname(req.Attempt))
	if err := os.MkdirAll(attemptDir, 0o700); err != nil {
		appendFailureAttempt(req.Loaded, attemptFailure{Phase: "attempt_setup", Kind: "attempt_setup_failed", Err: err, ArtifactDir: req.RunDir})
		return attemptReview{}, fmt.Errorf("create attempt dir %s: %w", attemptDir, err)
	}
	effectiveTask := req.EffectiveTask
	effectiveTaskPath := runartifact.Path(attemptDir, runartifact.EffectiveTaskSnapshotFilename)
	if err := task.Save(effectiveTaskPath, effectiveTask); err != nil {
		appendFailureAttempt(req.Loaded, attemptFailure{Phase: "attempt_setup", Kind: "attempt_setup_failed", Err: err, ArtifactDir: attemptDir})
		return attemptReview{}, err
	}
	// Load the runtime preflight result before the executor attempt so the
	// executor result and supervisor evidence share the same generated
	// skeleton bindings.
	preflightOutputs, err := skeletonpreflight.LoadResult(req.RunDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "galley: task %s attempt %d could not load preflight result: %v\n", req.Loaded.ID, req.Attempt, err)
	}
	outcome, err := runExecutorAttempt(ctx, executorAttemptRequest{Opts: req.Opts, Loaded: effectiveTask, WorkDir: req.Prepared.CWD, BaseSHA: req.Prepared.BaseSHA, AttemptDir: attemptDir, Prompt: req.Prompt})
	if err != nil {
		// A review-time staging failure is recorded under a distinct phase
		// and kind so the failed task surfaces the staging-related error to
		// the supervisor and operators instead of mis-classifying it as an
		// executor failure.
		if _, ok := asReviewStagingError(err); ok {
			appendFailureAttempt(req.Loaded, attemptFailure{Phase: "review_staging", Kind: "review_staging_failed", Err: err, ArtifactDir: attemptDir})
			return attemptReview{}, err
		}
		appendFailureAttempt(req.Loaded, attemptFailure{Phase: "executor", Kind: classifyFailureKind("executor_failed", err), Err: err, ArtifactDir: attemptDir})
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
	verdict, err := evaluateSupervisor(ctx, req.Opts, supervisorEvaluation{Evidence: evidence, AttemptDir: attemptDir, WorkDir: req.Prepared.CWD})
	if err != nil {
		appendSupervisorFailureAttempt(req.Loaded, outcome, err, attemptDir)
		return attemptReview{}, err
	}
	if err := writeJSON(runartifact.Path(attemptDir, runartifact.SupervisorVerdictFilename), verdict); err != nil {
		appendFailureAttempt(req.Loaded, attemptFailure{Phase: "run_evidence", Kind: "run_evidence_failed", Err: err, ArtifactDir: attemptDir})
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

func applySupervisorVerdict(execCtx, daemonCtx context.Context, req verdictApplication) (bool, error) {
	if daemonCtx.Err() != nil && req.Verdict.Status == "needs_revision" {
		fmt.Fprintf(os.Stderr, "galley: task %s stopped after attempt %d due to shutdown\n", req.Loaded.ID, req.Attempt)
		return true, failVerdictApplication(req, "shutdown", "Shutdown was requested after an attempt that needs revision; Galley did not start another retry attempt.", "Review the run evidence and requeue the task when ready.")
	}
	if daemonCtx.Err() != nil && req.Verdict.Status == "accepted" && req.Opts.CommitOnAccept {
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
		err := acceptSupervisorVerdict(execCtx, req)
		// A finalization failure keeps the accepted work, worktree, and review
		// progress and spends the next attempt of this loop repairing it.
		if failure, ok := asFinalizeFailure(err); ok {
			fmt.Fprintf(os.Stderr, "galley: task %s finalization failed after attempt %d; routing the failure into the revision loop: %v\n", req.Loaded.ID, req.Attempt, failure.Err)
			return false, recordFinalizeRevision(req, failure)
		}
		return true, err
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
	appendRisk(req.Loaded, riskPrefix, riskSpec{Type: "partial_verification", Detail: detail, Mitigation: mitigation, HumanReview: true})
	return taskstate.MoveToStatus(req.Opts.Root, req.RunningPath, req.Loaded)
}

func acceptSupervisorVerdict(ctx context.Context, req verdictApplication) error {
	opts, runningPath, loaded := req.Opts, req.RunningPath, req.Loaded
	profiles, verdict := req.Profiles, req.Verdict
	mergeDiscussionItems(loaded, profiles, verdict)
	*loaded = (supervisorRevision{}).applyToTask(*loaded)
	applyAcceptedAcceptanceCriteria(loaded)
	if err := finalizeOrCleanup(ctx, req); err != nil {
		return err
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
// supervisorEvaluation is one supervisor evaluation of an attempt.
type supervisorEvaluation struct {
	Evidence   supervisor.Evidence
	AttemptDir string
	WorkDir    string
}

func evaluateSupervisor(ctx context.Context, opts Options, req supervisorEvaluation) (supervisor.Verdict, error) {
	evidence, attemptDir, workDir := req.Evidence, req.AttemptDir, req.WorkDir
	artifactDir := runartifact.Path(attemptDir, runartifact.SupervisorTryDirname)
	if err := os.MkdirAll(artifactDir, 0o700); err != nil {
		return supervisor.Verdict{}, fmt.Errorf("create supervisor artifact dir %s: %w", artifactDir, err)
	}
	verdict, err := opts.daemonDependencies().supervisorRunner(ctx, supervisorRunRequest{Opts: opts, Evidence: evidence, TryDir: artifactDir, WorkDir: workDir})
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
		// A pending finalization request is decided by the finalize step that
		// runs right after this merge, not by a human PR reader.
		if request.Status == "addressed" || request.Source == finalizeRevisionSource {
			continue
		}
		appendDiscussionItem(
			loaded,
			"Unverified revision request",
			fmt.Sprintf("revision:%s (%s): %s", request.ID, request.Source, request.Text),
			true,
		)
	}
	if gaps := qualityReviewGaps(loaded, profiles.Quality); len(gaps) > 0 {
		appendDiscussionItem(loaded, "Quality review gaps", strings.Join(gaps, ", "), true)
	}
}

// qualityReviewGaps lists the quality review dimensions the task has not passed.
func qualityReviewGaps(loaded *task.Task, quality *profile.Quality) []string {
	if quality == nil {
		return nil
	}
	passed := map[string]bool{}
	if loaded.ReviewProgress != nil {
		for _, id := range loaded.ReviewProgress.Quality {
			passed[id] = true
		}
	}
	var gaps []string
	for _, dimension := range quality.ReviewDimensions {
		if !passed[dimension.ID] {
			gaps = append(gaps, dimension.ID)
		}
	}
	return gaps
}

func appendDiscussionItem(loaded *task.Task, topic, summary string, requiresHumanDecision bool) {
	if strings.TrimSpace(summary) == "" {
		return
	}
	// A second acceptance (for example after a recovered finalization) must not
	// duplicate the items the first acceptance already recorded.
	for _, item := range loaded.DiscussionItems {
		if item.Topic == topic && item.Summary == summary {
			return
		}
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
	return writeJSON(runartifact.Path(runDir, runartifact.SupervisorEvidenceFilename), map[string]string{
		"resolved":      effectiveOpts.Supervisor,
		"source":        effectiveOpts.SupervisorSource,
		"model":         effectiveOpts.SupervisorModel,
		"model_source":  modelSource,
		"effort":        effectiveOpts.SupervisorEffort,
		"effort_source": effortSource,
	})
}

// finalizeOnAccept commits and opens the PR. A failed commit, push, or PR
// creation returns to the verdict path; any other failure fails the task.
func finalizeOnAccept(ctx context.Context, req verdictApplication) error {
	opts, loaded, prepared := req.Opts, req.Loaded, req.Prepared
	fmt.Fprintf(os.Stderr, "galley: task %s accepted; finalizing commit/pr\n", loaded.ID)
	err := finalizeAcceptedChange(ctx, acceptedChange{Opts: opts, Loaded: loaded, WorkDir: prepared.CWD, BaseSHA: prepared.BaseSHA, RunDir: req.RunDir})
	if err == nil {
		markFinalizeRevisionsAddressed(loaded)
		return nil
	}
	if _, ok := asFinalizeFailure(err); ok {
		return err
	}
	loaded.Status = "failed"
	appendRisk(loaded, "finalize", riskSpec{
		Type:        "partial_verification",
		Detail:      err.Error(),
		Mitigation:  "The executor diff and run evidence were stored; a supervisor should inspect and finish commit or PR creation.",
		HumanReview: true,
	})
	return taskstate.FailMoveToStatus(opts.Root, req.RunningPath, loaded, err)
}

// cleanupNonCommittedOnAccept removes context-only inputs from a workspace that
// is kept without a commit.
func cleanupNonCommittedOnAccept(req verdictApplication) error {
	err := inputfiles.CleanupNonCommitted(req.Prepared.CWD, req.Loaded.Files)
	if err == nil {
		return nil
	}
	req.Loaded.Status = "failed"
	appendRisk(req.Loaded, "input-file-cleanup", riskSpec{
		Type:        "partial_verification",
		Detail:      err.Error(),
		Mitigation:  "Remove non-committed task input files from the execution workspace before archiving or reusing it.",
		HumanReview: true,
	})
	return taskstate.FailMoveToStatus(req.Opts.Root, req.RunningPath, req.Loaded, err)
}

// finalizeOrCleanup commits and opens the PR, or removes context-only inputs
// from the kept workspace when the daemon does not commit on accept.
func finalizeOrCleanup(ctx context.Context, req verdictApplication) error {
	if req.Opts.CommitOnAccept {
		return finalizeOnAccept(ctx, req)
	}
	return cleanupNonCommittedOnAccept(req)
}
