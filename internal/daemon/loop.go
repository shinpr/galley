package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shinpr/galley/internal/profile"
	"github.com/shinpr/galley/internal/result"
	"github.com/shinpr/galley/internal/runner"
	claudeguard "github.com/shinpr/galley/internal/runner/claude_guard_plugin"
	"github.com/shinpr/galley/internal/supervisor"
	"github.com/shinpr/galley/internal/task"
	"github.com/shinpr/galley/internal/workspace"
)

func runSupervisorLoop(ctx, shutdownCtx context.Context, opts Options, runningPath string, loaded *task.Task, prepared workspace.Prepared, runDir, runID string) error {
	fmt.Fprintf(os.Stderr, "galley: task %s running in %s (run_id=%s)\n", loaded.ID, prepared.CWD, runID)
	resolvedProfiles, err := resolveProfileFiles(opts, loaded.Scope.CWD)
	if err != nil {
		loaded.Status = "failed"
		return failTaskMove(opts.Root, runningPath, loaded, err)
	}
	profiles, err := profile.LoadBundle(resolvedProfiles.QualityProfileFile, resolvedProfiles.EnvironmentProfileFile)
	if err != nil {
		loaded.Status = "failed"
		return failTaskMove(opts.Root, runningPath, loaded, err)
	}
	if err := writeJSON(filepath.Join(runDir, "profiles.json"), struct {
		Resolved resolvedProfileFiles `json:"resolved"`
		Bundle   profile.Bundle       `json:"bundle"`
	}{Resolved: resolvedProfiles, Bundle: profiles}); err != nil {
		loaded.Status = "failed"
		return failTaskMove(opts.Root, runningPath, loaded, err)
	}
	prompt := task.RenderWorkOrderWithProfiles(executionTask(*loaded, prepared.CWD), profiles)
	budget := attemptBudget(loaded.ExecutionPolicy.LoopBudget)
	consecutiveNoDiff := 0
	for attempt := 1; budget < 0 || attempt <= budget; attempt++ {
		fmt.Fprintf(os.Stderr, "galley: task %s attempt %d/%s starting\n", loaded.ID, attempt, loaded.ExecutionPolicy.LoopBudget.String())
		attemptDir := filepath.Join(runDir, fmt.Sprintf("attempt-%d", attempt))
		if err := os.MkdirAll(attemptDir, 0o700); err != nil {
			loaded.Status = "failed"
			return failTaskMove(opts.Root, runningPath, loaded, fmt.Errorf("create attempt dir %s: %w", attemptDir, err))
		}
		effectiveTask := executionTask(*loaded, prepared.CWD)
		effectiveTaskPath := filepath.Join(attemptDir, "task.effective.yaml")
		if err := task.Save(effectiveTaskPath, effectiveTask); err != nil {
			loaded.Status = "failed"
			return failTaskMove(opts.Root, runningPath, loaded, err)
		}
		outcome, err := runExecutorAttempt(ctx, opts, effectiveTask, profiles, prepared.CWD, prepared.BaseSHA, attemptDir, prompt, effectiveTaskPath)
		if err != nil {
			loaded.Status = "failed"
			return failTaskMove(opts.Root, runningPath, loaded, err)
		}
		mergeAttemptEvidence(loaded, outcome, runID, prepared.CWD, attemptDir)
		if outcome.DiffErr == nil && !outcome.DiffDirty {
			consecutiveNoDiff++
		} else {
			consecutiveNoDiff = 0
		}
		evidence := supervisor.Evidence{
			Task:         *loaded,
			Profiles:     profiles,
			Claude:       outcome.ClaudeResult,
			ParseError:   outcome.ParseErr,
			RunError:     outcome.RunErr,
			DiffDirty:    outcome.DiffDirty,
			Diff:         outcome.Diff,
			DiffError:    outcome.DiffErr,
			Attempt:      attempt,
			AttemptsLeft: attemptsLeft(budget, attempt),
		}
		verdict, err := evaluateSupervisor(ctx, opts, evidence, attemptDir, prepared.CWD)
		if err != nil {
			loaded.Status = "failed"
			return failTaskMove(opts.Root, runningPath, loaded, err)
		}
		if err := writeJSON(filepath.Join(attemptDir, "supervisor_verdict.json"), verdict); err != nil {
			loaded.Status = "failed"
			return failTaskMove(opts.Root, runningPath, loaded, err)
		}
		fmt.Fprintf(os.Stderr, "galley: task %s attempt %d verdict=%s summary=%s\n", loaded.ID, attempt, verdict.Status, verdict.Summary)
		loaded.Attempts[len(loaded.Attempts)-1].SupervisorVerdict = verdict.Status
		loaded.Attempts[len(loaded.Attempts)-1].Summary = fmt.Sprintf("%s; run_id=%s; attempt=%d; workspace=%s", verdict.Summary, runID, attempt, prepared.CWD)
		if err := task.Save(runningPath, *loaded); err != nil {
			loaded.Status = "failed"
			return failTaskMove(opts.Root, runningPath, loaded, err)
		}
		if shutdownCtx.Err() != nil && verdict.Status == "needs_revision" {
			loaded.Status = "needs_supervisor_review"
			loaded.Risks = append(loaded.Risks, task.Risk{
				ID:                   fmt.Sprintf("shutdown-%d", len(loaded.Risks)+1),
				Type:                 "partial_verification",
				Detail:               "Shutdown was requested after an attempt that needs revision; Galley did not start another retry attempt.",
				Mitigation:           "Review the run evidence and requeue the task when ready.",
				HumanReviewSuggested: true,
			})
			fmt.Fprintf(os.Stderr, "galley: task %s stopped after attempt %d due to shutdown\n", loaded.ID, attempt)
			return moveTask(opts.Root, runningPath, "failed", loaded)
		}
		if verdict.Status == "needs_revision" && consecutiveNoDiff >= 2 {
			loaded.Status = "needs_supervisor_review"
			loaded.Risks = append(loaded.Risks, task.Risk{
				ID:                   fmt.Sprintf("progress-%d", len(loaded.Risks)+1),
				Type:                 "partial_verification",
				Detail:               "Two consecutive executor attempts produced no git diff.",
				Mitigation:           "A supervisor should inspect the task, work order, and executor logs before requeueing.",
				HumanReviewSuggested: true,
			})
			fmt.Fprintf(os.Stderr, "galley: task %s stopped by progress invariant: consecutive no-diff attempts\n", loaded.ID)
			return moveTask(opts.Root, runningPath, "failed", loaded)
		}

		switch verdict.Status {
		case "accepted":
			markRevisionRequestsAddressed(loaded, verdict.Summary)
			if opts.CommitOnAccept {
				fmt.Fprintf(os.Stderr, "galley: task %s accepted; finalizing commit/pr\n", loaded.ID)
				if err := finalizeAcceptedChange(ctx, opts, loaded, prepared.CWD, prepared.BaseSHA, runDir, runID); err != nil {
					loaded.Status = "needs_supervisor_review"
					loaded.Risks = append(loaded.Risks, task.Risk{
						ID:                   fmt.Sprintf("finalize-%d", len(loaded.Risks)+1),
						Type:                 "partial_verification",
						Detail:               err.Error(),
						Mitigation:           "The executor diff and run evidence were stored; a supervisor should inspect and finish commit or PR creation.",
						HumanReviewSuggested: true,
					})
					return failTaskMove(opts.Root, runningPath, loaded, err)
				}
			} else if err := cleanupNonCommittedInputFiles(prepared.CWD, loaded.Files); err != nil {
				loaded.Status = "needs_supervisor_review"
				loaded.Risks = append(loaded.Risks, task.Risk{
					ID:                   fmt.Sprintf("input-file-cleanup-%d", len(loaded.Risks)+1),
					Type:                 "partial_verification",
					Detail:               err.Error(),
					Mitigation:           "Remove non-committed task input files from the execution workspace before archiving or reusing it.",
					HumanReviewSuggested: true,
				})
				return failTaskMove(opts.Root, runningPath, loaded, err)
			}
			loaded.Status = "accepted"
			if opts.OpenPR {
				loaded.Status = "pr_opened"
			}
			fmt.Fprintf(os.Stderr, "galley: task %s completed with status=%s\n", loaded.ID, loaded.Status)
			return moveTask(opts.Root, runningPath, "done", loaded)
		case "needs_revision":
			prompt = verdict.NextWorkOrder
			continue
		case "hard_stop":
			loaded.Status = "failed"
			return moveTask(opts.Root, runningPath, "failed", loaded)
		case "needs_supervisor_review":
			loaded.Status = "needs_supervisor_review"
			return moveTask(opts.Root, runningPath, "failed", loaded)
		default:
			loaded.Status = "needs_supervisor_review"
			loaded.Risks = append(loaded.Risks, task.Risk{
				ID:                   fmt.Sprintf("supervisor-verdict-%d", len(loaded.Risks)+1),
				Type:                 "partial_verification",
				Detail:               fmt.Sprintf("Supervisor returned unknown verdict status %q.", verdict.Status),
				Mitigation:           "Inspect supervisor_verdict.json and rerun after correcting the supervisor output.",
				HumanReviewSuggested: true,
			})
			fmt.Fprintf(os.Stderr, "galley: task %s unknown supervisor verdict=%q\n", loaded.ID, verdict.Status)
			return moveTask(opts.Root, runningPath, "failed", loaded)
		}
	}
	loaded.Status = "needs_supervisor_review"
	fmt.Fprintf(os.Stderr, "galley: task %s exhausted attempts; needs supervisor review\n", loaded.ID)
	return moveTask(opts.Root, runningPath, "failed", loaded)
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
		ArtifactDir: attemptDir,
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
		Timeout:    time.Duration(loaded.ExecutionPolicy.TimeoutMS) * time.Millisecond,
		StdoutPath: stdoutPath,
		StderrPath: filepath.Join(attemptDir, "claude.stderr.log"),
	})
	completed := time.Now().UTC()
	if err := writeJSON(filepath.Join(attemptDir, "run_result.json"), runResult); err != nil {
		return attemptOutcome{}, err
	}

	resultPath := filepath.Join(attemptDir, "claude_result.json")
	claudeResult, parseErr := resolveClaudeResult(ctx, stdoutPath, runResult.Stdout, taskFile, resultPath, workDir, profiles)
	if parseErr == nil {
		if err := writeJSON(resultPath, claudeResult); err != nil {
			return attemptOutcome{}, err
		}
	}

	diffSnapshot, diffErr := workspace.CaptureSnapshotFromBase(ctx, workDir, baseSHA)
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

func resolveClaudeResult(ctx context.Context, stdoutPath, stdoutTail, taskFile, resultPath, workDir string, profiles profile.Bundle) (runner.ClaudeResult, error) {
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
	return runner.ClaudeResult{}, fmt.Errorf("resolve Claude result: verification evidence generation failed: %w; stdout file parse failed: %v; stdout tail parse failed: %v", generatedErr, claudeErr, tailErr)
}

func mergeExecutorJudgment(generated, reported runner.ClaudeResult) runner.ClaudeResult {
	if reported.Summary != "" {
		generated.Summary = generated.Summary + " Executor summary: " + reported.Summary
	}
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

func mapAcceptanceStatus(status string) string {
	switch status {
	case "satisfied":
		return "satisfied"
	case "partially_satisfied", "not_satisfied":
		// Task YAML stores conservative task state; the full Claude status remains in claude_result.json.
		return "not_satisfied"
	default:
		return "unknown"
	}
}

func attemptBudget(b task.LoopBudget) int {
	if b.Infinite {
		return -1
	}
	if b.Count > 0 {
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
