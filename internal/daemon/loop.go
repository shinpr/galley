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
	"github.com/shinpr/galley/internal/profile"
	"github.com/shinpr/galley/internal/result"
	"github.com/shinpr/galley/internal/runner"
	claudeguard "github.com/shinpr/galley/internal/runner/claude_guard_plugin"
	"github.com/shinpr/galley/internal/supervisor"
	"github.com/shinpr/galley/internal/task"
	"github.com/shinpr/galley/internal/taskstate"
	"github.com/shinpr/galley/internal/workspace"
)

const progressNoDiffThreshold = 2

func runSupervisorLoop(ctx, shutdownCtx context.Context, opts Options, runningPath string, loaded *task.Task, prepared workspace.Prepared, profiles profile.Bundle, runDir, runID string) error {
	fmt.Fprintf(os.Stderr, "galley: task %s running in %s (run_id=%s)\n", loaded.ID, prepared.CWD, runID)
	// processClaimedTask resolved profiles before workspace creation and
	// already wrote runs/<run-id>/profiles.json. Threading the resolved
	// bundle through this function keeps the supervisor loop from
	// double-loading the environment profile.
	effectiveOpts := effectiveOptionsForProfiles(opts, profiles)
	prompt := task.RenderWorkOrderWithProfiles(executionTask(*loaded, prepared.CWD), profiles)
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
			return taskstate.FailMove(opts.Root, runningPath, loaded, err)
		}
		mergeAttemptEvidence(loaded, review.Outcome, runID, prepared.CWD, review.AttemptDir)
		if review.Outcome.DiffErr == nil && !review.Outcome.DiffDirty {
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
	outcome, err := runExecutorAttempt(ctx, req.Opts, effectiveTask, req.Profiles, req.Prepared.CWD, req.Prepared.BaseSHA, attemptDir, req.Prompt, effectiveTaskPath)
	if err != nil {
		appendFailureAttempt(req.Loaded, "executor", classifyFailureKind("executor_failed", err), err, attemptDir)
		return attemptReview{}, err
	}
	evidence := supervisor.Evidence{
		Task:         *req.Loaded,
		Profiles:     req.Profiles,
		Claude:       outcome.ClaudeResult,
		ParseError:   outcome.ParseErr,
		RunError:     outcome.RunErr,
		DiffDirty:    outcome.DiffDirty,
		Diff:         outcome.Diff,
		DiffError:    outcome.DiffErr,
		Attempt:      req.Attempt,
		AttemptsLeft: attemptsLeft(req.Budget, req.Attempt),
	}
	verdict, err := evaluateSupervisor(ctx, req.Opts, evidence, attemptDir, req.Prepared.CWD)
	if err != nil {
		appendSupervisorFailureAttempt(req.Loaded, outcome, err, attemptDir)
		return attemptReview{}, err
	}
	if err := writeJSON(filepath.Join(attemptDir, "supervisor_verdict.json"), verdict); err != nil {
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
		req.Loaded.Status = "needs_supervisor_review"
		req.Loaded.Risks = append(req.Loaded.Risks, task.Risk{
			ID:                   fmt.Sprintf("shutdown-%d", len(req.Loaded.Risks)+1),
			Type:                 "partial_verification",
			Detail:               "Shutdown was requested after an attempt that needs revision; Galley did not start another retry attempt.",
			Mitigation:           "Review the run evidence and requeue the task when ready.",
			HumanReviewSuggested: true,
		})
		fmt.Fprintf(os.Stderr, "galley: task %s stopped after attempt %d due to shutdown\n", req.Loaded.ID, req.Attempt)
		return "", true, taskstate.Move(req.Opts.Root, req.RunningPath, "failed", req.Loaded)
	}
	if shutdownCtx.Err() != nil && req.Verdict.Status == "accepted" && req.Opts.CommitOnAccept {
		req.Loaded.Status = "needs_supervisor_review"
		req.Loaded.Risks = append(req.Loaded.Risks, task.Risk{
			ID:                   fmt.Sprintf("shutdown-finalize-%d", len(req.Loaded.Risks)+1),
			Type:                 "partial_verification",
			Detail:               "Shutdown was requested before accepted work was finalized; Galley skipped commit, push, and PR creation to avoid an interrupted external side effect.",
			Mitigation:           "Inspect the accepted diff and requeue or finalize manually when ready.",
			HumanReviewSuggested: true,
		})
		fmt.Fprintf(os.Stderr, "galley: task %s accepted during shutdown; skipped finalization\n", req.Loaded.ID)
		return "", true, taskstate.Move(req.Opts.Root, req.RunningPath, "failed", req.Loaded)
	}
	if req.Verdict.Status == "needs_revision" && req.ConsecutiveNoDiff >= progressNoDiffThreshold {
		req.Loaded.Status = "needs_supervisor_review"
		req.Loaded.Risks = append(req.Loaded.Risks, task.Risk{
			ID:                   fmt.Sprintf("progress-%d", len(req.Loaded.Risks)+1),
			Type:                 "partial_verification",
			Detail:               "Two consecutive executor attempts produced no git diff.",
			Mitigation:           "A supervisor should inspect the task, work order, and executor logs before requeueing.",
			HumanReviewSuggested: true,
		})
		fmt.Fprintf(os.Stderr, "galley: task %s stopped by progress invariant: consecutive no-diff attempts\n", req.Loaded.ID)
		return "", true, taskstate.Move(req.Opts.Root, req.RunningPath, "failed", req.Loaded)
	}

	switch req.Verdict.Status {
	case "accepted":
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
		req.Loaded.Status = "needs_supervisor_review"
		req.Loaded.Risks = append(req.Loaded.Risks, task.Risk{
			ID:                   fmt.Sprintf("supervisor-verdict-%d", len(req.Loaded.Risks)+1),
			Type:                 "partial_verification",
			Detail:               fmt.Sprintf("Supervisor returned unknown verdict status %q.", req.Verdict.Status),
			Mitigation:           "Inspect supervisor_verdict.json and rerun after correcting the supervisor output.",
			HumanReviewSuggested: true,
		})
		fmt.Fprintf(os.Stderr, "galley: task %s unknown supervisor verdict=%q\n", req.Loaded.ID, req.Verdict.Status)
		return "", true, taskstate.Move(req.Opts.Root, req.RunningPath, "failed", req.Loaded)
	}
}

func acceptSupervisorVerdict(ctx context.Context, opts Options, runningPath string, loaded *task.Task, prepared workspace.Prepared, runDir string, verdict supervisor.Verdict) error {
	markRevisionRequestsAddressed(loaded, verdict.Summary)
	applyAcceptedAcceptanceCriteria(loaded, verdict)
	mergeDiscussionItems(loaded, verdict)
	if opts.CommitOnAccept {
		fmt.Fprintf(os.Stderr, "galley: task %s accepted; finalizing commit/pr\n", loaded.ID)
		if err := finalizeAcceptedChange(ctx, opts, loaded, prepared.CWD, prepared.BaseSHA, runDir); err != nil {
			loaded.Status = "needs_supervisor_review"
			loaded.Risks = append(loaded.Risks, task.Risk{
				ID:                   fmt.Sprintf("finalize-%d", len(loaded.Risks)+1),
				Type:                 "partial_verification",
				Detail:               err.Error(),
				Mitigation:           "The executor diff and run evidence were stored; a supervisor should inspect and finish commit or PR creation.",
				HumanReviewSuggested: true,
			})
			return taskstate.FailMove(opts.Root, runningPath, loaded, err)
		}
	} else if err := inputfiles.CleanupNonCommitted(prepared.CWD, loaded.Files); err != nil {
		loaded.Status = "needs_supervisor_review"
		loaded.Risks = append(loaded.Risks, task.Risk{
			ID:                   fmt.Sprintf("input-file-cleanup-%d", len(loaded.Risks)+1),
			Type:                 "partial_verification",
			Detail:               err.Error(),
			Mitigation:           "Remove non-committed task input files from the execution workspace before archiving or reusing it.",
			HumanReviewSuggested: true,
		})
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

func evaluateSupervisor(ctx context.Context, opts Options, evidence supervisor.Evidence, attemptDir, workDir string) (supervisor.Verdict, error) {
	verdict, err := supervisor.RunAdapter(ctx, supervisor.AdapterOptions{
		Provider:    opts.Supervisor,
		WorkDir:     workDir,
		Timeout:     time.Duration(evidence.Task.ExecutionPolicy.TimeoutMS) * time.Millisecond,
		IdleTimeout: opts.IdleTimeout,
		ArtifactDir: attemptDir,
		ClaudeBin:   opts.ClaudeBin,
		CodexBin:    opts.CodexBin,
	}, evidence)
	if err != nil {
		return supervisor.Verdict{}, err
	}
	if err := writeJSON(filepath.Join(attemptDir, "model_supervisor_verdict.json"), verdict); err != nil {
		return supervisor.Verdict{}, err
	}
	return verdict, nil
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
}

func runExecutorAttempt(ctx context.Context, opts Options, loaded task.Task, profiles profile.Bundle, workDir, baseSHA, attemptDir, prompt, taskFile string) (attemptOutcome, error) {
	claudeOpts := runner.FromTask(loaded)
	claudeOpts.Bin = opts.ClaudeBin
	claudeOpts.WorkDir = workDir
	claudeOpts.SystemPromptFile = opts.SystemPromptFile
	claudeOpts.JSONSchemaFile = opts.JSONSchemaFile
	claudeOpts.Prompt = prompt
	if !opts.DisableClaudeGuard {
		guardDir := opts.ClaudeGuardPluginDir
		if guardDir == "" {
			guardDir = filepath.Join(opts.Root, "runtime", "claude-guard-plugin")
		}
		guardDir, err := claudeguard.Ensure(guardDir)
		if err != nil {
			return attemptOutcome{}, err
		}
		guardDir, err = filepath.Abs(guardDir)
		if err != nil {
			return attemptOutcome{}, fmt.Errorf("resolve Claude guard plugin dir: %w", err)
		}
		claudeOpts.PluginDirs = append(claudeOpts.PluginDirs, guardDir)
	}

	commandPlan, err := runner.ClaudeCommandPlan(claudeOpts)
	if err != nil {
		return attemptOutcome{}, err
	}
	if err := writeJSON(filepath.Join(attemptDir, "command_plan.json"), commandPlan); err != nil {
		return attemptOutcome{}, err
	}

	started := time.Now().UTC()
	stdoutPath := filepath.Join(attemptDir, "claude.stdout.jsonl")
	runResult, runErr := runner.RunCommand(ctx, commandPlan, runner.RunOptions{
		Timeout:     time.Duration(loaded.ExecutionPolicy.TimeoutMS) * time.Millisecond,
		IdleTimeout: opts.IdleTimeout,
		StdoutPath:  stdoutPath,
		StderrPath:  filepath.Join(attemptDir, "claude.stderr.log"),
	})
	completed := time.Now().UTC()
	if err := writeJSON(filepath.Join(attemptDir, "run_result.json"), runResult); err != nil {
		return attemptOutcome{}, err
	}

	resultPath := filepath.Join(attemptDir, "claude_result.json")
	claudeResult, parseErr := resolveClaudeResult(ctx, opts, stdoutPath, runResult.Stdout, taskFile, resultPath, workDir, profiles)
	if parseErr == nil {
		if err := writeJSON(resultPath, claudeResult); err != nil {
			return attemptOutcome{}, err
		}
	}

	diffSnapshot, diffErr := workspace.CaptureSnapshotFromBase(ctx, workDir, baseSHA, workspaceOptions(opts))
	diffDirty := false
	diffText := ""
	if diffErr == nil {
		diffDirty = diffSnapshot.Dirty
		diffText = diffSnapshot.Diff
		if err := writeJSON(filepath.Join(attemptDir, "git_status.json"), diffSnapshot); err != nil {
			return attemptOutcome{}, err
		}
		if err := os.WriteFile(filepath.Join(attemptDir, "diff.patch"), []byte(diffSnapshot.Diff), 0o600); err != nil {
			return attemptOutcome{}, fmt.Errorf("write diff.patch: %w", err)
		}
	}

	return attemptOutcome{
		Started:      started,
		Completed:    completed,
		RunResult:    runResult,
		RunErr:       runErr,
		ClaudeResult: claudeResult,
		ParseErr:     parseErr,
		DiffDirty:    diffDirty,
		Diff:         diffText,
		DiffErr:      diffErr,
	}, nil
}

func resolveClaudeResult(ctx context.Context, opts Options, stdoutPath, stdoutTail, taskFile, resultPath, workDir string, profiles profile.Bundle) (runner.ClaudeResult, error) {
	claudeResult, claudeErr := runner.ExtractClaudeResultFile(stdoutPath)
	if claudeErr == nil && claudeResult.Status == "hard_stop" {
		return claudeResult, nil
	}
	generated, generatedErr := result.Complete(ctx, result.CompleteOptions{
		TaskFile: taskFile,
		Output:   resultPath,
		WorkDir:  workDir,
		Summary:  "Task implementation completed and verification evidence was recorded by Galley.",
		Profiles: profiles,
		GitBin:   opts.GitBin,
	})
	if generatedErr == nil {
		if claudeErr == nil {
			return mergeExecutorJudgment(generated, claudeResult), nil
		}
		return generated, nil
	}
	if claudeErr == nil {
		return claudeResult, nil
	}
	tailResult, tailErr := runner.ExtractClaudeResult(stdoutTail)
	if tailErr == nil {
		return tailResult, nil
	}
	return runner.ClaudeResult{}, errors.Join(
		fmt.Errorf("verification evidence generation failed: %w", generatedErr),
		fmt.Errorf("stdout file parse failed: %w", claudeErr),
		fmt.Errorf("stdout tail parse failed: %w", tailErr),
	)
}

func mergeExecutorJudgment(generated, reported runner.ClaudeResult) runner.ClaudeResult {
	if reported.Summary != "" {
		generated.Summary = generated.Summary + " Executor summary: " + reported.Summary
	}
	if len(reported.AcceptanceCriteria) > 0 {
		generated.AcceptanceCriteria = reported.AcceptanceCriteria
	}
	generated.Verification = append(reported.Verification, generated.Verification...)
	generated.Decisions = append(generated.Decisions, reported.Decisions...)
	generated.Risks = append(generated.Risks, reported.Risks...)
	if reported.Status == "completed_with_risks" && generated.Status == "completed" && len(reported.Risks) > 0 {
		generated.Status = "completed_with_risks"
	}
	return generated
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
		Cmd:           "claude -p",
		Status:        verificationStatus(outcome.RunErr),
		OutputExcerpt: fmt.Sprintf("executor stdout/stderr captured under %s; run_result.json contains bounded tails", attemptDir),
	})
	if outcome.DiffErr != nil {
		loaded.Risks = append(loaded.Risks, task.Risk{
			ID:                   fmt.Sprintf("git-diff-%d", len(loaded.Risks)+1),
			Type:                 "partial_verification",
			Detail:               outcome.DiffErr.Error(),
			Mitigation:           "Stored other run evidence; git diff evidence is unavailable.",
			HumanReviewSuggested: true,
		})
	}
	if outcome.ParseErr != nil {
		loaded.Risks = append(loaded.Risks, task.Risk{
			ID:                   fmt.Sprintf("claude-result-parse-%d", len(loaded.Risks)+1),
			Type:                 "partial_verification",
			Detail:               outcome.ParseErr.Error(),
			Mitigation:           "Stored raw Claude stdout and stderr for supervisor review.",
			HumanReviewSuggested: true,
		})
		return
	}
	if outcome.ClaudeResult.Status == "completed" && outcome.DiffErr == nil && !outcome.DiffDirty {
		loaded.Risks = append(loaded.Risks, task.Risk{
			ID:                   fmt.Sprintf("git-diff-empty-%d", len(loaded.Risks)+1),
			Type:                 "partial_verification",
			Detail:               "Executor completed but produced no git diff in the execution workspace.",
			Mitigation:           "Stored Claude result and raw logs for supervisor review.",
			HumanReviewSuggested: true,
		})
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
		loaded.Risks = append(loaded.Risks, task.Risk{
			ID:                   fmt.Sprintf("claude-risk-%d", len(loaded.Risks)+1),
			Type:                 claudeRisk.Type,
			Detail:               claudeRisk.Detail,
			Mitigation:           claudeRisk.Mitigation,
			HumanReviewSuggested: claudeRisk.NeedsHumanReview,
		})
	}
	if outcome.ClaudeResult.Status == "hard_stop" && outcome.ClaudeResult.HardStop != nil {
		loaded.Risks = append(loaded.Risks, task.Risk{
			ID:                   fmt.Sprintf("claude-hard-stop-%d", len(loaded.Risks)+1),
			Type:                 "other",
			Detail:               outcome.ClaudeResult.HardStop.Reason,
			Mitigation:           strings.Join(outcome.ClaudeResult.HardStop.NeededToContinue, "; "),
			HumanReviewSuggested: true,
		})
	}
}

func executorAttemptError(outcome attemptOutcome, attemptDir string) *task.AttemptError {
	if outcome.RunErr == nil {
		return nil
	}
	return attemptError("executor", classifyFailureKind("executor_failed", outcome.RunErr), outcome.RunErr, attemptDir)
}

func appendSupervisorFailureAttempt(loaded *task.Task, outcome attemptOutcome, err error, attemptDir string) {
	loaded.Attempts = append(loaded.Attempts, task.Attempt{
		Number:            len(loaded.Attempts) + 1,
		StartedAt:         outcome.Started.Format(time.RFC3339Nano),
		CompletedAt:       time.Now().UTC().Format(time.RFC3339Nano),
		ClaudeStatus:      claudeStatus(outcome.RunResult, outcome.RunErr),
		SupervisorVerdict: classifyFailureKind("supervisor_failed", err),
		Summary:           err.Error(),
		Error:             attemptError("supervisor", classifyFailureKind("supervisor_failed", err), err, attemptDir),
	})
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
