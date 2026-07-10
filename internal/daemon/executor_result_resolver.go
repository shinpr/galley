package daemon

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/shinpr/galley/internal/runner"
)

// codexLastMessagePath returns the attempt-scoped `--output-last-message`
// capture path Galley requests for Codex executor runs (empty for Claude
// because Claude streams its result directly to stdout JSONL).
func codexLastMessagePath(cli, attemptDir string) string {
	if cli != "codex" || attemptDir == "" {
		return ""
	}
	return filepath.Join(attemptDir, runner.CodexLastMessageFilename)
}

// resolveExecutorResult resolves the structured executor result for one attempt.
// For Claude executor runs the result lives in the JSONL stdout stream; for
// Codex executor runs the canonical surface is the final message captured by
// `codex exec --output-last-message`, with stdout parsing only as an extraction
// fallback. Galley does not synthesize executor results when no valid final
// JSON exists.
func resolveExecutorResult(cli, stdoutPath, stdoutTail, lastMessagePath string) (runner.ClaudeResult, error) {
	var resultErrs []error
	if cli == "codex" && lastMessagePath != "" {
		if lastResult, lastErr := runner.ExtractCodexLastMessageFile(lastMessagePath); lastErr == nil {
			return lastResult, nil
		} else {
			resultErrs = append(resultErrs, fmt.Errorf("codex last-message parse failed: %w", lastErr))
		}
	}

	claudeResult, claudeErr := runner.ExtractClaudeResultFile(stdoutPath)
	if claudeErr == nil {
		return claudeResult, nil
	}
	tailResult, tailErr := runner.ExtractClaudeResult(stdoutTail)
	if tailErr == nil {
		return tailResult, nil
	}
	resultErrs = append(resultErrs,
		fmt.Errorf("stdout file parse failed: %w", claudeErr),
		fmt.Errorf("stdout tail parse failed: %w", tailErr),
	)
	return runner.ClaudeResult{}, errors.Join(resultErrs...)
}
