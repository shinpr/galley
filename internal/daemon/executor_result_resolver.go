package daemon

import (
	"errors"
	"fmt"
	"os"
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
func resolveExecutorResult(cli, stdoutPath, stdoutTail, lastMessagePath string) (runner.ExecutorResult, error) {
	if cli == "grok" {
		data, err := os.ReadFile(stdoutPath)
		if err != nil {
			data = []byte(stdoutTail)
		}
		return runner.ExtractGrokExecutorResult(data)
	}
	var resultErrs []error
	if cli == "codex" && lastMessagePath != "" {
		if lastResult, lastErr := runner.ExtractCodexLastMessageFile(lastMessagePath); lastErr == nil {
			return lastResult, nil
		} else {
			resultErrs = append(resultErrs, fmt.Errorf("codex last-message parse failed: %w", lastErr))
		}
	}

	fileResult, fileErr := runner.ExtractExecutorResultFile(stdoutPath)
	if fileErr == nil {
		return fileResult, nil
	}
	tailResult, tailErr := runner.ExtractExecutorResult(stdoutTail)
	if tailErr == nil {
		return tailResult, nil
	}
	resultErrs = append(resultErrs,
		fmt.Errorf("stdout file parse failed: %w", fileErr),
		fmt.Errorf("stdout tail parse failed: %w", tailErr),
	)
	return runner.ExecutorResult{}, errors.Join(resultErrs...)
}

// classifyExecutorTerminal derives one routing decision per executor exit from
// runner state plus machine-readable provider output, reading captured stdout
// with an in-memory tail fallback. Process exit code or human-language error
// text alone never decides routing.
func classifyExecutorTerminal(cli, stdoutPath, stdoutTail string, runErr error) runner.ExecutorTerminal {
	switch cli {
	case "grok":
		data, err := os.ReadFile(stdoutPath)
		if err != nil {
			data = []byte(stdoutTail)
		}
		return runner.GrokTerminal(data, runErr)
	case "codex":
		return runner.CodexTerminal(readExecutorStdout(stdoutPath, stdoutTail), runErr)
	default:
		// cli is "claude", "glm", or empty (defaulting to claude); GLM shares
		// Claude's transport but keeps its own provider identity in evidence.
		provider := cli
		if provider == "" {
			provider = "claude"
		}
		return runner.ClaudeTerminal(provider, readExecutorStdout(stdoutPath, stdoutTail), runErr)
	}
}

func readExecutorStdout(stdoutPath, stdoutTail string) []byte {
	if data, err := os.ReadFile(stdoutPath); err == nil {
		return data
	}
	return []byte(stdoutTail)
}
