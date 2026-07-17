package daemon

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/shinpr/galley/internal/runner"
	"github.com/shinpr/galley/internal/task"
)

// executorInterruptedError signals that an executor attempt was interrupted
// (provider or runtime failure) before it reached a normal provider terminal.
// The loop uses it to stop before supervisor review and publish the retained
// task to tasks/failed without inventing a verdict.
type executorInterruptedError struct {
	Reason string
	Err    error
}

func (e *executorInterruptedError) Error() string {
	if e == nil {
		return "executor interrupted"
	}
	reason := e.Reason
	if reason == "" {
		reason = runner.TerminalReasonUnknown
	}
	if e.Err != nil {
		return fmt.Sprintf("executor interrupted (%s): %s", reason, e.Err.Error())
	}
	return fmt.Sprintf("executor interrupted (%s)", reason)
}

func (e *executorInterruptedError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func asExecutorInterrupted(err error) (*executorInterruptedError, bool) {
	var ie *executorInterruptedError
	if errors.As(err, &ie) {
		return ie, true
	}
	return nil, false
}

// appendInterruptedAttempt records the executor-owned interruption: a
// not_reviewed attempt with a distinct executor_interrupted error, a failed
// verification entry, and a recovery risk. It sets status failed so the retained
// worktree and evidence are published for requeue.
func appendInterruptedAttempt(loaded *task.Task, outcome attemptOutcome, attemptDir string) *executorInterruptedError {
	reason := outcome.Terminal.Reason
	if reason == "" {
		reason = runner.TerminalReasonUnknown
	}
	message := interruptionMessage(outcome, reason)
	loaded.Attempts = append(loaded.Attempts, task.Attempt{
		Number:            len(loaded.Attempts) + 1,
		StartedAt:         outcome.Started.Format(time.RFC3339Nano),
		CompletedAt:       outcome.Completed.Format(time.RFC3339Nano),
		ClaudeStatus:      interruptedExecutorStatus(outcome.RunResult, outcome.RunErr),
		SupervisorVerdict: task.AttemptVerdictNotReviewed,
		Summary:           message,
		Error: &task.AttemptError{
			Phase:       "executor",
			Kind:        task.AttemptKindExecutorInterrupted,
			Message:     message,
			ArtifactDir: attemptDir,
		},
	})
	loaded.Verification.Commands = append(loaded.Verification.Commands, task.VerificationCommand{
		Cmd:           executorVerificationCmd(outcome.CLI),
		Status:        "failed",
		OutputExcerpt: interruptionEvidenceExcerpt(outcome, reason, attemptDir),
	})
	appendRisk(loaded, "executor-interrupted", "partial_verification", message,
		"Partial work and run evidence are preserved in the retained worktree and attempt directory; resolve the interruption cause, then run `galley task requeue` to reuse the worktree and start a fresh executor attempt.", true)
	appendInterruptedArtifactRisks(loaded, outcome)
	loaded.Status = task.StatusFailed
	return &executorInterruptedError{Reason: reason, Err: outcome.RunErr}
}

// interruptedArtifactRequeueMitigation is the shared recovery guidance for every
// interruption-path artifact capture failure: the retained worktree still holds
// the underlying changes and a requeue reuses it.
const interruptedArtifactRequeueMitigation = "The retained worktree still holds the underlying changes; resolve the cause, then run `galley task requeue` to reuse the worktree and start a fresh executor attempt."

// appendInterruptedArtifactRisks records a distinct risk for each attempt
// artifact whose write failed on the interruption path, so operators know
// exactly which evidence is missing. The git status/diff capture failure keeps
// the dedicated "interrupted-diff-capture" ID and raw error detail.
func appendInterruptedArtifactRisks(loaded *task.Task, outcome attemptOutcome) {
	if outcome.RunResultErr != nil {
		appendRisk(loaded, "interrupted-artifact-capture", "partial_verification",
			"run_result.json could not be written: "+outcome.RunResultErr.Error(), interruptedArtifactRequeueMitigation, true)
	}
	if outcome.TerminalErr != nil {
		appendRisk(loaded, "interrupted-artifact-capture", "partial_verification",
			"executor_terminal.json could not be written: "+outcome.TerminalErr.Error(), interruptedArtifactRequeueMitigation, true)
	}
	if outcome.GrokMetaErr != nil {
		appendRisk(loaded, "interrupted-artifact-capture", "partial_verification",
			"grok_completion.json could not be written: "+outcome.GrokMetaErr.Error(), interruptedArtifactRequeueMitigation, true)
	}
	if outcome.ResultErr != nil {
		appendRisk(loaded, "interrupted-artifact-capture", "partial_verification",
			"executor_result.json could not be written: "+outcome.ResultErr.Error(), interruptedArtifactRequeueMitigation, true)
	}
	if outcome.StagingErr != nil {
		appendRisk(loaded, "interrupted-review-staging", "partial_verification",
			"review staging failed: "+outcome.StagingErr.Error(), interruptedArtifactRequeueMitigation, true)
	}
	if outcome.DiffErr != nil {
		appendRisk(loaded, "interrupted-diff-capture", "partial_verification", outcome.DiffErr.Error(),
			"Some git status/diff evidence could not be written for this interrupted attempt; the retained worktree still holds the changes. Resolve the cause, then run `galley task requeue` to reuse the worktree.", true)
	}
}

// interruptionEvidenceExcerpt lists which attempt artifacts were preserved and
// which were not, so `galley task show` never claims an artifact a write failure
// prevented persisting. It reports real per-artifact status because the runner
// outcome and terminal survive every failure.
func interruptionEvidenceExcerpt(outcome attemptOutcome, reason, attemptDir string) string {
	preserved, failed := interruptionArtifactStatus(outcome)
	excerpt := fmt.Sprintf("executor interrupted (%s); preserved under %s: %s", reason, attemptDir, strings.Join(preserved, ", "))
	if len(failed) > 0 {
		excerpt += "; not captured: " + strings.Join(failed, "; ")
	}
	return excerpt
}

func interruptionArtifactStatus(outcome attemptOutcome) (preserved, failed []string) {
	appendArtifact := func(name string, err error) {
		if err == nil {
			preserved = append(preserved, name)
			return
		}
		failed = append(failed, fmt.Sprintf("%s (%s)", name, err.Error()))
	}
	appendArtifact("raw provider output", outcome.RawOutputErr)
	appendArtifact("run_result.json", outcome.RunResultErr)
	appendArtifact("executor_terminal.json", outcome.TerminalErr)
	if outcome.CLI == "grok" {
		appendArtifact("grok_completion.json", outcome.GrokMetaErr)
	}
	if outcome.ParseErr == nil {
		appendArtifact("executor_result.json", outcome.ResultErr)
	}
	if outcome.StagingErr != nil {
		failed = append(failed, "review staging ("+outcome.StagingErr.Error()+")")
	}
	appendArtifact("git_status.json", outcome.GitStatusErr)
	appendArtifact("diff.patch", outcome.DiffPatchErr)
	return preserved, failed
}

// interruptionMessage renders a concise, operator-facing interruption summary.
// Provider detail is appended when available and otherwise falls back to the
// generic reason without changing routing.
func interruptionMessage(outcome attemptOutcome, reason string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Executor interrupted before supervisor review (%s).", reason)
	detail := outcome.Terminal.Detail
	var parts []string
	if detail.Status != "" {
		parts = append(parts, "status="+detail.Status)
	}
	if detail.Code != "" {
		parts = append(parts, "code="+detail.Code)
	}
	if detail.StopReason != "" {
		parts = append(parts, "stop_reason="+detail.StopReason)
	}
	if detail.SessionID != "" {
		parts = append(parts, "session_id="+detail.SessionID)
	}
	if detail.Message != "" {
		parts = append(parts, "message="+detail.Message)
	}
	if len(parts) > 0 {
		fmt.Fprintf(&b, " provider %s.", strings.Join(parts, " "))
	} else if outcome.RunErr != nil {
		fmt.Fprintf(&b, " %s.", outcome.RunErr.Error())
	}
	return b.String()
}

// interruptedExecutorStatus keeps the runner-derived status for interruptions
// that carry a runner error (idle_timed_out/timed_out/failed) and labels
// provider-terminal interruptions that exited cleanly as "interrupted" so the
// attempt status never reads as "completed".
func interruptedExecutorStatus(result runner.RunResult, runErr error) string {
	if runErr != nil {
		return executorStatus(result, runErr)
	}
	return "interrupted"
}

func appendFailureAttempt(loaded *task.Task, phase, kind string, err error, artifactDir string) {
	if loaded == nil {
		return
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	message := ""
	if err != nil {
		message = err.Error()
	}
	loaded.Attempts = append(loaded.Attempts, task.Attempt{
		Number:            len(loaded.Attempts) + 1,
		StartedAt:         now,
		CompletedAt:       now,
		ClaudeStatus:      "not_run",
		SupervisorVerdict: kind,
		Summary:           message,
		Error:             attemptError(phase, kind, err, artifactDir),
	})
}

func attemptError(phase, kind string, err error, artifactDir string) *task.AttemptError {
	message := ""
	if err != nil {
		message = err.Error()
	}
	return &task.AttemptError{
		Phase:       phase,
		Kind:        kind,
		Message:     message,
		ArtifactDir: artifactDir,
	}
}

func classifyFailureKind(defaultKind string, err error) string {
	if err == nil {
		return defaultKind
	}
	// Classify via the runner's typed sentinels rather than error-message
	// substrings so a wording change in the runner cannot silently reclassify
	// an idle/total timeout as a generic executor failure and hide the
	// actionable timeout signal from downstream recovery.
	if errors.Is(err, runner.ErrIdleTimeout) {
		return "idle_timeout"
	}
	if errors.Is(err, runner.ErrTimeout) || errors.Is(err, context.DeadlineExceeded) {
		return "timed_out"
	}
	return defaultKind
}
