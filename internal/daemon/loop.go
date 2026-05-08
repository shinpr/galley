package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shinpr/galley/internal/profile"
	"github.com/shinpr/galley/internal/runner"
	"github.com/shinpr/galley/internal/supervisor"
	"github.com/shinpr/galley/internal/task"
	"github.com/shinpr/galley/internal/workspace"
)

func runSupervisorLoop(ctx context.Context, opts Options, runningPath string, loaded *task.Task, prepared workspace.Prepared, runDir, runID string) error {
	profiles, err := profile.LoadBundle(opts.QualityProfileFile, opts.EnvironmentProfileFile)
	if err != nil {
		loaded.Status = "failed"
		_ = moveTask(opts.Root, runningPath, "failed", loaded)
		return err
	}
	if err := writeJSON(filepath.Join(runDir, "profiles.json"), profiles); err != nil {
		loaded.Status = "failed"
		_ = moveTask(opts.Root, runningPath, "failed", loaded)
		return err
	}
	prompt := task.RenderWorkOrderWithProfiles(*loaded, profiles)
	budget := attemptBudget(loaded.ExecutionPolicy.LoopBudget)
	for attempt := 1; budget < 0 || attempt <= budget; attempt++ {
		attemptDir := filepath.Join(runDir, fmt.Sprintf("attempt-%d", attempt))
		if err := os.MkdirAll(attemptDir, 0o755); err != nil {
			loaded.Status = "failed"
			_ = moveTask(opts.Root, runningPath, "failed", loaded)
			return fmt.Errorf("create attempt dir %s: %w", attemptDir, err)
		}
		outcome, err := runExecutorAttempt(ctx, opts, *loaded, prepared.CWD, attemptDir, prompt)
		if err != nil {
			loaded.Status = "failed"
			_ = moveTask(opts.Root, runningPath, "failed", loaded)
			return err
		}
		mergeAttemptEvidence(loaded, outcome, runID, prepared.CWD)
		evidence := supervisor.Evidence{
			Task:         *loaded,
			Claude:       outcome.ClaudeResult,
			ParseError:   outcome.ParseErr,
			RunError:     outcome.RunErr,
			DiffDirty:    outcome.DiffDirty,
			DiffError:    outcome.DiffErr,
			Attempt:      attempt,
			AttemptsLeft: attemptsLeft(budget, attempt),
		}
		verdict, err := evaluateSupervisor(ctx, opts, evidence, attemptDir, prepared.CWD)
		if err != nil {
			loaded.Status = "failed"
			_ = moveTask(opts.Root, runningPath, "failed", loaded)
			return err
		}
		if err := writeJSON(filepath.Join(attemptDir, "supervisor_verdict.json"), verdict); err != nil {
			loaded.Status = "failed"
			_ = moveTask(opts.Root, runningPath, "failed", loaded)
			return err
		}
		loaded.Attempts[len(loaded.Attempts)-1].SupervisorVerdict = verdict.Status
		loaded.Attempts[len(loaded.Attempts)-1].Summary = fmt.Sprintf("%s; run_id=%s; attempt=%d; workspace=%s", verdict.Summary, runID, attempt, prepared.CWD)
		if err := task.Save(runningPath, *loaded); err != nil {
			loaded.Status = "failed"
			_ = moveTask(opts.Root, runningPath, "failed", loaded)
			return err
		}

		switch verdict.Status {
		case "accepted":
			if opts.CommitOnAccept {
				if err := finalizeAcceptedChange(ctx, opts, loaded, prepared.CWD, runDir, runID); err != nil {
					loaded.Status = "needs_supervisor_review"
					loaded.Risks = append(loaded.Risks, task.Risk{
						ID:                   fmt.Sprintf("finalize-%d", len(loaded.Risks)+1),
						Type:                 "partial_verification",
						Detail:               err.Error(),
						Mitigation:           "The executor diff and run evidence were stored; a supervisor should inspect and finish commit or PR creation.",
						HumanReviewSuggested: true,
					})
					_ = moveTask(opts.Root, runningPath, "failed", loaded)
					return err
				}
			}
			loaded.Status = "accepted"
			if opts.OpenPR {
				loaded.Status = "pr_opened"
			}
			return moveTask(opts.Root, runningPath, "done", loaded)
		case "needs_revision":
			prompt = verdict.NextWorkOrder
			continue
		case "hard_stop":
			loaded.Status = "failed"
			return moveTask(opts.Root, runningPath, "failed", loaded)
		default:
			loaded.Status = "needs_supervisor_review"
			return moveTask(opts.Root, runningPath, "failed", loaded)
		}
	}
	loaded.Status = "needs_supervisor_review"
	return moveTask(opts.Root, runningPath, "failed", loaded)
}

func evaluateSupervisor(ctx context.Context, opts Options, evidence supervisor.Evidence, attemptDir, workDir string) (supervisor.Verdict, error) {
	if len(opts.SupervisorCommand) == 0 {
		return supervisor.Evaluate(evidence), nil
	}
	verdict, err := supervisor.RunExternal(ctx, supervisor.ExternalOptions{
		Argv:    opts.SupervisorCommand,
		WorkDir: workDir,
		Timeout: time.Duration(evidence.Task.ExecutionPolicy.TimeoutMS) * time.Millisecond,
	}, evidence)
	if err != nil {
		return supervisor.Verdict{}, err
	}
	if err := writeJSON(filepath.Join(attemptDir, "external_supervisor_verdict.json"), verdict); err != nil {
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
	DiffErr      error
}

func runExecutorAttempt(ctx context.Context, opts Options, loaded task.Task, workDir, attemptDir, prompt string) (attemptOutcome, error) {
	claudeOpts := runner.FromTask(loaded)
	claudeOpts.WorkDir = workDir
	claudeOpts.SystemPromptFile = opts.SystemPromptFile
	claudeOpts.JSONSchemaFile = opts.JSONSchemaFile
	claudeOpts.Prompt = prompt

	commandPlan, err := runner.ClaudeCommandPlan(claudeOpts)
	if err != nil {
		return attemptOutcome{}, err
	}
	if err := writeJSON(filepath.Join(attemptDir, "command_plan.json"), commandPlan); err != nil {
		return attemptOutcome{}, err
	}

	started := time.Now().UTC()
	runResult, runErr := runner.RunCommand(ctx, commandPlan, runner.RunOptions{
		Timeout:    time.Duration(loaded.ExecutionPolicy.TimeoutMS) * time.Millisecond,
		StdoutPath: filepath.Join(attemptDir, "claude.stdout.jsonl"),
		StderrPath: filepath.Join(attemptDir, "claude.stderr.log"),
	})
	completed := time.Now().UTC()
	if err := writeJSON(filepath.Join(attemptDir, "run_result.json"), runResult); err != nil {
		return attemptOutcome{}, err
	}

	diffSnapshot, diffErr := workspace.CaptureSnapshot(ctx, workDir)
	diffDirty := false
	if diffErr == nil {
		diffDirty = diffSnapshot.Dirty
		if err := writeJSON(filepath.Join(attemptDir, "git_status.json"), diffSnapshot); err != nil {
			return attemptOutcome{}, err
		}
		if err := os.WriteFile(filepath.Join(attemptDir, "diff.patch"), []byte(diffSnapshot.Diff), 0o644); err != nil {
			return attemptOutcome{}, fmt.Errorf("write diff.patch: %w", err)
		}
	}

	claudeResult, parseErr := runner.ExtractClaudeResult(runResult.Stdout)
	if parseErr == nil {
		if err := writeJSON(filepath.Join(attemptDir, "claude_result.json"), claudeResult); err != nil {
			return attemptOutcome{}, err
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
		DiffErr:      diffErr,
	}, nil
}

func mergeAttemptEvidence(loaded *task.Task, outcome attemptOutcome, runID, workDir string) {
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
		OutputExcerpt: outcome.RunResult.Stdout,
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
	return 1
}

func attemptsLeft(budget, attempt int) int {
	if budget < 0 {
		return 1
	}
	return budget - attempt
}
