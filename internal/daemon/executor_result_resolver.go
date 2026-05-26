package daemon

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/shinpr/galley/internal/profile"
	"github.com/shinpr/galley/internal/result"
	"github.com/shinpr/galley/internal/runner"
)

func codexLastMessagePath(cli, attemptDir string) string {
	if cli != "codex" || attemptDir == "" {
		return ""
	}
	return filepath.Join(attemptDir, runner.CodexLastMessageFilename)
}

// resolveExecutorResult resolves the structured executor result for one
// attempt. For Claude executor runs the result lives in the JSONL stdout
// stream; for Codex executor runs the canonical surface is the final message
// captured by `codex exec --output-last-message`, with the JSONL stdout stream
// as a fallback.
func resolveExecutorResult(ctx context.Context, opts Options, cli, stdoutPath, stdoutTail, lastMessagePath, taskFile, resultPath, workDir string, profiles profile.Bundle) (runner.ClaudeResult, error) {
	if cli == "codex" && lastMessagePath != "" {
		if lastResult, lastErr := runner.ExtractCodexLastMessageFile(lastMessagePath); lastErr == nil {
			if lastResult.Status == "hard_stop" {
				return lastResult, nil
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
				return mergeExecutorJudgment(generated, lastResult), nil
			}
			return lastResult, nil
		}
	}

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
