package daemon

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/shinpr/galley/internal/executorflow"
	skeletonpreflight "github.com/shinpr/galley/internal/preflight/skeleton"
	"github.com/shinpr/galley/internal/profile"
	"github.com/shinpr/galley/internal/runartifact"
	"github.com/shinpr/galley/internal/runner"
	claudeguard "github.com/shinpr/galley/internal/runner/claude_guard_plugin"
	"github.com/shinpr/galley/internal/task"
	"github.com/shinpr/galley/internal/vcs"
	"github.com/shinpr/galley/internal/workspace"
)

type attemptOutcome struct {
	Started        time.Time
	Completed      time.Time
	RunResult      runner.RunResult
	RunErr         error
	ExecutorResult runner.ExecutorResult
	ParseErr       error
	DiffDirty      bool
	Diff           string
	DiffErr        error
	DiffSnapshot   workspace.Snapshot
}

func defaultStageExecutorOutput(ctx context.Context, opts Options, workDir, attemptDir string, excludePaths []string) error {
	bins := vcsBinaries(opts)
	statusZ, err := vcs.StatusPorcelainZ(ctx, bins, workDir)
	if err != nil {
		return err
	}
	reviewable := reviewablePathsFromStatus(statusZ, excludePaths)
	return vcs.StagePathsForReview(ctx, bins, workDir, attemptDir, reviewable)
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
	executorResult, parseErr := resolveExecutorResult(cli, stdoutPath, run.RunResult.Stdout, lastMessagePath)
	if parseErr == nil {
		if err := writeJSON(resultPath, executorResult); err != nil {
			return attemptOutcome{}, err
		}
	}

	// Stage untracked executor output, excluding context-only inputs, before
	// supervisor review. The parent context preserves evidence after a timeout.
	excludePaths := nonCommittedInputDestinations(loaded.Files)
	if err := opts.daemonDependencies().stageExecutorOutput(ctx, opts, workDir, attemptDir, excludePaths); err != nil {
		return attemptOutcome{}, &reviewStagingError{Err: err}
	}

	diffArtifacts, err := executorflow.CaptureDiffArtifacts(ctx, workDir, baseSHA, attemptDir, workspaceOptions(opts))
	if err != nil {
		return attemptOutcome{}, err
	}

	return attemptOutcome{
		Started:        run.Started,
		Completed:      run.Completed,
		RunResult:      run.RunResult,
		RunErr:         run.RunErr,
		ExecutorResult: executorResult,
		ParseErr:       parseErr,
		DiffDirty:      diffArtifacts.Dirty,
		Diff:           diffArtifacts.Diff,
		DiffErr:        diffArtifacts.Err,
		DiffSnapshot:   diffArtifacts.Snapshot,
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
		ClaudeStatus:      executorStatus(outcome.RunResult, outcome.RunErr),
		SupervisorVerdict: "not_reviewed",
		Summary:           fmt.Sprintf("Executor run %s; run_id=%s; workspace=%s", executorStatus(outcome.RunResult, outcome.RunErr), runID, workDir),
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
		appendRisk(loaded, "executor-result-parse", "partial_verification", outcome.ParseErr.Error(), fmt.Sprintf("Stored raw %s stdout and stderr for supervisor review.", executorArtifactLabel(loaded.Executor.CLI)), true)
		return
	}
	if outcome.ExecutorResult.Status == "completed" && outcome.DiffErr == nil && !outcome.DiffDirty {
		appendRisk(loaded, "git-diff-empty", "partial_verification", "Executor completed but produced no git diff in the execution workspace.", fmt.Sprintf("Stored %s result and raw logs for supervisor review.", executorArtifactLabel(loaded.Executor.CLI)), true)
	}
	for _, ac := range outcome.ExecutorResult.AcceptanceCriteria {
		for i := range loaded.AcceptanceCriteria {
			if loaded.AcceptanceCriteria[i].ID == ac.ID {
				loaded.AcceptanceCriteria[i].Status = mapAcceptanceStatus(ac.Status)
			}
		}
	}
	for _, verification := range outcome.ExecutorResult.Verification {
		loaded.Verification.Commands = append(loaded.Verification.Commands, task.VerificationCommand{
			Cmd:           verification.Command,
			Status:        verification.Status,
			OutputExcerpt: verification.OutputExcerpt,
		})
	}
	for _, decision := range outcome.ExecutorResult.Decisions {
		loaded.Decisions = append(loaded.Decisions, task.Decision{
			ID:               fmt.Sprintf("executor-decision-%d", len(loaded.Decisions)+1),
			Question:         decision.Question,
			Chosen:           decision.Chosen,
			Rationale:        decision.Rationale,
			Reversibility:    decision.Reversibility,
			NeedsHumanReview: decision.NeedsHumanReview,
		})
	}
	for _, executorRisk := range outcome.ExecutorResult.Risks {
		appendRisk(loaded, "executor-risk", executorRisk.Type, executorRisk.Detail, executorRisk.Mitigation, executorRisk.NeedsHumanReview)
	}
	if outcome.ExecutorResult.Status == "hard_stop" && outcome.ExecutorResult.HardStop != nil {
		appendRisk(loaded, "executor-hard-stop", "other", outcome.ExecutorResult.HardStop.Reason, strings.Join(outcome.ExecutorResult.HardStop.NeededToContinue, "; "), true)
	}
}

func executorArtifactLabel(cli string) string {
	if cli == "" {
		return "executor"
	}
	return cli + " executor"
}

func executorVerificationCmd(cli string) string {
	switch cli {
	case "codex":
		return "codex exec"
	case "glm":
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
	if idle, ok := asSupervisorIdleTimeout(err); ok {
		appendSupervisorIdleTimeoutAttempt(loaded, outcome, idle, attemptDir)
		return
	}
	kind := classifyFailureKind("supervisor_failed", err)
	loaded.Attempts = append(loaded.Attempts, task.Attempt{
		Number:            len(loaded.Attempts) + 1,
		StartedAt:         outcome.Started.Format(time.RFC3339Nano),
		CompletedAt:       time.Now().UTC().Format(time.RFC3339Nano),
		ClaudeStatus:      executorStatus(outcome.RunResult, outcome.RunErr),
		SupervisorVerdict: kind,
		Summary:           err.Error(),
		Error:             attemptError("supervisor", kind, err, attemptDir),
	})
	// A supervisor process failure is not a verdict on the executor's work.
	loaded.Status = "needs_supervisor_review"
	appendRisk(loaded, "supervisor-stall", "partial_verification", fmt.Sprintf("Supervisor evaluation failed (%s): %s", kind, err.Error()), "Inspect the supervisor-try-N evidence under the attempt directory and requeue the task once the supervisor backend is healthy.", true)
}

func appendSupervisorIdleTimeoutAttempt(loaded *task.Task, outcome attemptOutcome, idle *supervisorIdleTimeoutError, attemptDir string) {
	message := idle.attemptErrorMessage()
	loaded.Attempts = append(loaded.Attempts, task.Attempt{
		Number:            len(loaded.Attempts) + 1,
		StartedAt:         outcome.Started.Format(time.RFC3339Nano),
		CompletedAt:       time.Now().UTC().Format(time.RFC3339Nano),
		ClaudeStatus:      executorStatus(outcome.RunResult, outcome.RunErr),
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

// GLM uses Claude's transport and evidence format with a redirected endpoint.
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
