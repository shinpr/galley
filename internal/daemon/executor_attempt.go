package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shinpr/galley/internal/executorflow"
	"github.com/shinpr/galley/internal/proc"
	"github.com/shinpr/galley/internal/provider"
	"github.com/shinpr/galley/internal/runartifact"
	"github.com/shinpr/galley/internal/runner"
	claudeguard "github.com/shinpr/galley/internal/runner/claude_guard_plugin"
	"github.com/shinpr/galley/internal/supervisor"
	"github.com/shinpr/galley/internal/task"
	"github.com/shinpr/galley/internal/vcs"
	"github.com/shinpr/galley/internal/workspace"
)

type attemptOutcome struct {
	Started        time.Time
	Completed      time.Time
	RunResult      proc.RunResult
	RunErr         error
	Terminal       runner.ExecutorTerminal
	ExecutorResult runner.ExecutorResult
	ParseErr       error
	DiffDirty      bool
	Diff           string
	DiffErr        error
	DiffSnapshot   workspace.Snapshot
	// EvidenceErr records a post-interruption staging or diff-capture failure. It
	// is secondary evidence: the interruption stays primary so the task still
	// routes to tasks/failed and requeue recovery stays available.
	EvidenceErr error
}

// evidenceCaptureTimeout bounds post-executor git status and diff capture. The
// capture uses a context detached from the executor so a shutdown that cancels
// the executor cannot cancel evidence capture; the bound stops a wedged git
// process from blocking shutdown.
const evidenceCaptureTimeout = 2 * time.Minute

// stageOutputRequest is one attempt's executor output staging.
type stageOutputRequest struct {
	Opts         Options
	WorkDir      string
	AttemptDir   string
	ExcludePaths []string
}

func defaultStageExecutorOutput(ctx context.Context, req stageOutputRequest) error {
	excludePaths := req.ExcludePaths
	repo := vcsRepo(req.Opts, req.WorkDir, req.AttemptDir)
	statusZ, err := vcs.StatusPorcelainZ(ctx, repo)
	if err != nil {
		return err
	}
	reviewable := reviewablePathsFromStatus(statusZ, excludePaths)
	return vcs.StagePathsForReview(ctx, repo, reviewable)
}

// executorAttemptRequest is one executor attempt in a prepared workspace.
type executorAttemptRequest struct {
	Opts       Options
	Loaded     task.Task
	WorkDir    string
	BaseSHA    string
	AttemptDir string
	Prompt     string
}

func runExecutorAttempt(ctx context.Context, req executorAttemptRequest) (attemptOutcome, error) {
	opts, loaded := req.Opts, req.Loaded
	workDir, baseSHA, attemptDir, prompt := req.WorkDir, req.BaseSHA, req.AttemptDir, req.Prompt
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
		commandPlan proc.Command
		stdoutPath  string
		stderrPath  string
		err         error
	)
	transport, ok := provider.TransportFor(cli)
	if !ok || !provider.IsExecutor(cli) {
		return attemptOutcome{}, fmt.Errorf("unsupported executor.cli %q; must be one of: %s", cli, strings.Join(task.ExecutorCLIEnum(), ", "))
	}
	switch transport {
	case provider.TransportClaude:
		commandPlan, stdoutPath, stderrPath, err = prepareClaudeExecutorPlan(executorPlanRequest{Opts: opts, Loaded: loaded, WorkDir: workDir, Prompt: prompt, AttemptDir: attemptDir})
		if err == nil {
			err = runner.ConfigureClaudeProvider(&commandPlan, claudeProviderOptions(cli, opts))
		}
	case provider.TransportCodex:
		commandPlan, stdoutPath, stderrPath, err = prepareCodexExecutorPlan(executorPlanRequest{Opts: opts, Loaded: loaded, WorkDir: workDir, Prompt: prompt, AttemptDir: attemptDir})
	case provider.TransportGrok:
		commandPlan, stdoutPath, stderrPath, err = prepareGrokExecutorPlan(executorPlanRequest{Opts: opts, Loaded: loaded, WorkDir: workDir, Prompt: prompt, AttemptDir: attemptDir})
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
	if transport == provider.TransportGrok {
		if err := writeGrokCompletionEvidence(attemptDir, stdoutPath, run.RunResult.Stdout); err != nil {
			return attemptOutcome{}, err
		}
	}

	resultPath := runartifact.Path(attemptDir, runartifact.ExecutorResultFilename)
	lastMessagePath := codexLastMessagePath(cli, attemptDir)
	executorResult, parseErr := resolveExecutorResult(cli, stdoutPath, run.RunResult.Stdout, lastMessagePath)
	if parseErr == nil {
		if err := writeJSON(resultPath, executorResult); err != nil {
			return attemptOutcome{}, err
		}
	}

	// The normal-terminal versus interruption decision is derived from runner
	// state and provider output and persisted as attempt evidence before the
	// task is routed. runExecutorAttempt still captures diff, git status, and raw
	// logs below so an interrupted attempt keeps its full evidence set.
	terminal := classifyExecutorTerminal(cli, stdoutPath, run.RunResult.Stdout, run.RunErr)
	if err := writeJSON(runartifact.Path(attemptDir, runartifact.ExecutorTerminalFilename), terminal); err != nil {
		return attemptOutcome{}, err
	}

	outcome := attemptOutcome{
		Started:        run.Started,
		Completed:      run.Completed,
		RunResult:      run.RunResult,
		RunErr:         run.RunErr,
		Terminal:       terminal,
		ExecutorResult: executorResult,
		ParseErr:       parseErr,
	}

	evidenceCtx, evidenceCancel := context.WithTimeout(context.WithoutCancel(ctx), evidenceCaptureTimeout)
	defer evidenceCancel()

	excludePaths := nonCommittedInputDestinations(loaded.Files)
	stageErr := opts.daemonDependencies().stageExecutorOutput(evidenceCtx, stageOutputRequest{Opts: opts, WorkDir: workDir, AttemptDir: attemptDir, ExcludePaths: excludePaths})
	if stageErr != nil && !terminal.Interrupted() {
		// For a normally completed attempt a staging failure is terminal: the
		// supervisor would otherwise review a stale or empty diff.
		return attemptOutcome{}, &reviewStagingError{Err: stageErr}
	}
	if stageErr != nil {
		// The interruption stays the primary outcome; still attempt diff capture
		// so git status/diff evidence is retained where possible.
		outcome.EvidenceErr = stageErr
	}

	diffArtifacts, err := opts.daemonDependencies().captureDiffArtifacts(evidenceCtx, executorflow.DiffCapture{WorkDir: workDir, BaseSHA: baseSHA, AttemptDir: attemptDir, Opts: workspaceOptions(opts)})
	if err != nil {
		if !terminal.Interrupted() {
			return attemptOutcome{}, err
		}
		outcome.EvidenceErr = errors.Join(outcome.EvidenceErr, err)
		return outcome, nil
	}
	outcome.DiffDirty = diffArtifacts.Dirty
	outcome.Diff = diffArtifacts.Diff
	outcome.DiffErr = diffArtifacts.Err
	outcome.DiffSnapshot = diffArtifacts.Snapshot
	return outcome, nil
}

// attemptEvidencePaths locate one attempt's evidence.
type attemptEvidencePaths struct {
	RunID      string
	WorkDir    string
	AttemptDir string
}

func mergeAttemptEvidence(loaded *task.Task, outcome attemptOutcome, paths attemptEvidencePaths) {
	runID, workDir, attemptDir := paths.RunID, paths.WorkDir, paths.AttemptDir
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
		appendRisk(loaded, "git-diff", riskSpec{Type: "partial_verification", Detail: outcome.DiffErr.Error(), Mitigation: "Stored other run evidence; git diff evidence is unavailable.", HumanReview: true})
	}
	if outcome.ParseErr != nil {
		appendRisk(loaded, "executor-result-parse", riskSpec{Type: "partial_verification", Detail: outcome.ParseErr.Error(), Mitigation: fmt.Sprintf("Stored raw %s stdout and stderr for supervisor review.", executorArtifactLabel(loaded.Executor.CLI)), HumanReview: true})
		return
	}
	if outcome.ExecutorResult.Status == "completed" && outcome.DiffErr == nil && !outcome.DiffDirty {
		appendRisk(loaded, "git-diff-empty", riskSpec{Type: "partial_verification", Detail: "Executor completed but produced no git diff in the execution workspace.", Mitigation: fmt.Sprintf("Stored %s result and raw logs for supervisor review.", executorArtifactLabel(loaded.Executor.CLI)), HumanReview: true})
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
		appendRisk(loaded, "executor-risk", riskSpec{Type: executorRisk.Type, Detail: executorRisk.Detail, Mitigation: executorRisk.Mitigation, HumanReview: executorRisk.NeedsHumanReview})
	}
	if outcome.ExecutorResult.Status == "hard_stop" && outcome.ExecutorResult.HardStop != nil {
		appendRisk(loaded, "executor-hard-stop", riskSpec{Type: "other", Detail: outcome.ExecutorResult.HardStop.Reason, Mitigation: strings.Join(outcome.ExecutorResult.HardStop.NeededToContinue, "; "), HumanReview: true})
	}
}

func executorArtifactLabel(cli string) string {
	if cli == "" {
		return "executor"
	}
	return cli + " executor"
}

func executorVerificationCmd(cli string) string {
	if cli == "" {
		return "claude -p"
	}
	transport, _ := provider.TransportFor(cli)
	switch transport {
	case provider.TransportCodex:
		return "codex exec"
	case provider.TransportGrok:
		return "grok"
	case provider.TransportClaude:
		if cli != "claude" {
			return fmt.Sprintf("claude -p (%s)", cli)
		}
		return "claude -p"
	}
	return "unknown"
}

func executorAttemptError(outcome attemptOutcome, attemptDir string) *task.AttemptError {
	if outcome.RunErr == nil {
		return nil
	}
	return attemptError(attemptFailure{Phase: "executor", Kind: classifyFailureKind("executor_failed", outcome.RunErr), Err: outcome.RunErr, ArtifactDir: attemptDir})
}

// executorInterruptionError signals that an executor attempt did not reach a
// normal provider terminal. The daemon loop publishes the task to tasks/failed
// without invoking Supervisor or starting another executor attempt.
type executorInterruptionError struct {
	terminal runner.ExecutorTerminal
}

func (e *executorInterruptionError) Error() string {
	if e == nil {
		return "executor interrupted"
	}
	return "executor interrupted: " + e.terminal.Reason
}

func asExecutorInterruptionError(err error) (*executorInterruptionError, bool) {
	var interruption *executorInterruptionError
	if errors.As(err, &interruption) {
		return interruption, true
	}
	return nil, false
}

// interruptionKind maps an interrupted attempt to its persisted attempt-error
// kind. Runner failures keep the timeout/idle/executor classifications so
// recovery still sees the actionable signal; a provider-reported non-normal
// terminal with a successful runner records the distinct executor_interrupted
// kind.
func interruptionKind(outcome attemptOutcome) string {
	if outcome.RunErr != nil {
		return classifyFailureKind("executor_failed", outcome.RunErr)
	}
	return "executor_interrupted"
}

// appendExecutorInterruptionAttempt records the interrupted attempt with an
// executor-owned structured error, a non-verdict marker (no Supervisor produced
// one), and a requeue recovery risk. Provider detail is retained for diagnosis
// only; it never changes routing.
func appendExecutorInterruptionAttempt(loaded *task.Task, outcome attemptOutcome, attemptDir string) {
	message := interruptionMessage(outcome.Terminal)
	loaded.Attempts = append(loaded.Attempts, task.Attempt{
		Number:            len(loaded.Attempts) + 1,
		StartedAt:         outcome.Started.Format(time.RFC3339Nano),
		CompletedAt:       outcome.Completed.Format(time.RFC3339Nano),
		ClaudeStatus:      executorStatus(outcome.RunResult, outcome.RunErr),
		SupervisorVerdict: "not_reviewed",
		Summary:           message,
		Error: &task.AttemptError{
			Phase:       "executor",
			Kind:        interruptionKind(outcome),
			Message:     message,
			ArtifactDir: attemptDir,
		},
	})
	loaded.Verification.Commands = append(loaded.Verification.Commands, task.VerificationCommand{
		Cmd:           executorVerificationCmd(loaded.Executor.CLI),
		Status:        "failed",
		OutputExcerpt: fmt.Sprintf("executor interrupted (%s); terminal decision and raw logs captured under %s", outcome.Terminal.Reason, attemptDir),
	})
	appendRisk(loaded, "executor-interruption", riskSpec{Type: "external_dependency", Detail: message, Mitigation: "Resolve the interruption cause, then run `galley task requeue` to resume from the preserved worktree.", HumanReview: true})
	// CaptureDiffArtifacts reports a snapshot/diff failure in-band as DiffErr (nil
	// function error), so it is joined with any staging failure. Both surface as
	// secondary evidence; the interruption stays primary and the worktree keeps
	// partial changes for requeue.
	if evidenceErr := errors.Join(outcome.EvidenceErr, outcome.DiffErr); evidenceErr != nil {
		appendRisk(loaded, "executor-interruption-evidence", riskSpec{Type: "partial_verification", Detail: evidenceErr.Error(), Mitigation: "Evidence capture failed after the interruption; the preserved worktree still holds the partial changes for requeue.", HumanReview: true})
	}
}

// interruptionMessage renders an operator-facing interruption reason, keeping
// available provider detail and falling back to a generic reason when none
// exists.
func interruptionMessage(terminal runner.ExecutorTerminal) string {
	parts := []string{fmt.Sprintf("Executor interrupted before Supervisor review (reason=%s)", terminal.Reason)}
	if terminal.RunError != "" {
		parts = append(parts, fmt.Sprintf("run_error=%s", terminal.RunError))
	}
	if terminal.Status != "" {
		parts = append(parts, fmt.Sprintf("status=%s", terminal.Status))
	}
	if terminal.Code != "" {
		parts = append(parts, fmt.Sprintf("code=%s", terminal.Code))
	}
	if terminal.StopReason != "" {
		parts = append(parts, fmt.Sprintf("stop_reason=%s", terminal.StopReason))
	}
	if terminal.SessionID != "" {
		parts = append(parts, fmt.Sprintf("session_id=%s", terminal.SessionID))
	}
	if terminal.Message != "" {
		parts = append(parts, fmt.Sprintf("detail=%s", terminal.Message))
	}
	return strings.Join(parts, "; ")
}

func appendSupervisorFailureAttempt(loaded *task.Task, outcome attemptOutcome, err error, attemptDir string) {
	if idle, ok := asSupervisorIdleTimeout(err); ok {
		appendSupervisorIdleTimeoutAttempt(loaded, outcome, idle, attemptDir)
		return
	}
	kind := supervisorFailureKind(err)
	loaded.Attempts = append(loaded.Attempts, task.Attempt{
		Number:            len(loaded.Attempts) + 1,
		StartedAt:         outcome.Started.Format(time.RFC3339Nano),
		CompletedAt:       time.Now().UTC().Format(time.RFC3339Nano),
		ClaudeStatus:      executorStatus(outcome.RunResult, outcome.RunErr),
		SupervisorVerdict: kind,
		Summary:           err.Error(),
		Error:             attemptError(attemptFailure{Phase: "supervisor", Kind: kind, Err: err, ArtifactDir: attemptDir}),
	})
	// A supervisor failure is not a human review decision. Preserve the
	// evidence as an operational failure so the task can be requeued.
	loaded.Status = "failed"
	if supervisor.IsVerdictContractError(err) {
		appendRisk(loaded, "supervisor-invalid-verdict", riskSpec{Type: "partial_verification", Detail: fmt.Sprintf("Supervisor evaluation failed (%s): %s", kind, err.Error()), Mitigation: "Inspect the supervisor-try-1 validation evidence and requeue with the same or another supervisor after correcting the output-contract issue.", HumanReview: true})
		return
	}
	appendRisk(loaded, "supervisor-stall", riskSpec{Type: "partial_verification", Detail: fmt.Sprintf("Supervisor evaluation failed (%s): %s", kind, err.Error()), Mitigation: "Inspect the supervisor-try-1 evidence under the attempt directory and requeue the task once the supervisor backend is healthy.", HumanReview: true})
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
	loaded.Status = "failed"
	appendRisk(loaded, "supervisor-idle-timeout", riskSpec{Type: "partial_verification", Detail: message, Mitigation: "Inspect the supervisor-try-1 evidence under the attempt directory, then requeue the task or adjust the daemon --idle-timeout or --supervisor settings.", HumanReview: true})
}

// executorPlanRequest is the input to one executor's command plan.
type executorPlanRequest struct {
	Opts       Options
	Loaded     task.Task
	WorkDir    string
	Prompt     string
	AttemptDir string
}

func prepareClaudeExecutorPlan(req executorPlanRequest) (proc.Command, string, string, error) {
	opts, loaded := req.Opts, req.Loaded
	workDir, prompt, attemptDir := req.WorkDir, req.Prompt, req.AttemptDir
	claudeOpts := runner.FromTask(loaded)
	claudeOpts.Bin = opts.ClaudeBin
	claudeOpts.WorkDir = workDir
	claudeOpts.SystemPromptFile = opts.SystemPromptFile
	claudeOpts.JSONSchemaFile = opts.JSONSchemaFile
	claudeOpts.AttemptDir = attemptDir
	claudeOpts.Prompt = prompt
	if !opts.DisableClaudeGuard {
		guardDir, err := resolveClaudeGuardDir(opts)
		if err != nil {
			return proc.Command{}, "", "", err
		}
		claudeOpts.PluginDirs = append(claudeOpts.PluginDirs, guardDir)
	}
	plan, err := runner.ClaudeCommandPlan(claudeOpts)
	if err != nil {
		return proc.Command{}, "", "", err
	}
	return plan, filepath.Join(attemptDir, "claude.stdout.jsonl"), filepath.Join(attemptDir, "claude.stderr.log"), nil
}

func prepareCodexExecutorPlan(req executorPlanRequest) (proc.Command, string, string, error) {
	opts, loaded := req.Opts, req.Loaded
	workDir, prompt, attemptDir := req.WorkDir, req.Prompt, req.AttemptDir
	codexOpts := runner.CodexFromTask(loaded)
	codexOpts.Bin = opts.CodexBin
	codexOpts.WorkDir = workDir
	codexOpts.SystemPromptFile = opts.SystemPromptFile
	codexOpts.JSONSchemaFile = opts.JSONSchemaFile
	codexOpts.AttemptDir = attemptDir
	codexOpts.Prompt = prompt
	plan, err := runner.CodexCommandPlan(codexOpts)
	if err != nil {
		return proc.Command{}, "", "", err
	}
	return plan, filepath.Join(attemptDir, "codex.stdout.jsonl"), filepath.Join(attemptDir, "codex.stderr.log"), nil
}

func prepareGrokExecutorPlan(req executorPlanRequest) (proc.Command, string, string, error) {
	opts, loaded := req.Opts, req.Loaded
	workDir, prompt, attemptDir := req.WorkDir, req.Prompt, req.AttemptDir
	grokOpts := runner.GrokFromTask(loaded)
	grokOpts.Bin = opts.GrokBin
	grokOpts.WorkDir = workDir
	grokOpts.SystemPromptFile = opts.SystemPromptFile
	grokOpts.JSONSchemaFile = opts.JSONSchemaFile
	grokOpts.AttemptDir = attemptDir
	grokOpts.Prompt = prompt
	plan, err := runner.GrokCommandPlan(grokOpts)
	if err != nil {
		return proc.Command{}, "", "", err
	}
	return plan, filepath.Join(attemptDir, "grok.stdout.json"), filepath.Join(attemptDir, "grok.stderr.log"), nil
}

// writeGrokCompletionEvidence records Grok's completion metadata, falling back
// to the captured stdout tail when the stdout file cannot be read.
func writeGrokCompletionEvidence(attemptDir, stdoutPath, stdoutTail string) error {
	data, err := os.ReadFile(stdoutPath)
	if err != nil {
		data = []byte(stdoutTail)
	}
	return runner.WriteGrokCompletionMetadata(runartifact.Path(attemptDir, runartifact.GrokCompletionMetadataFilename), data)
}

// resolveClaudeGuardDir materializes the Claude guard plugin and returns its
// absolute path.
func resolveClaudeGuardDir(opts Options) (string, error) {
	guardDir := opts.ClaudeGuardPluginDir
	if guardDir == "" {
		guardDir = filepath.Join(opts.Root, "runtime", "claude-guard-plugin")
	}
	guardDir, err := claudeguard.Ensure(guardDir)
	if err != nil {
		return "", err
	}
	guardDir, err = filepath.Abs(guardDir)
	if err != nil {
		return "", fmt.Errorf("resolve Claude guard plugin dir: %w", err)
	}
	return guardDir, nil
}
