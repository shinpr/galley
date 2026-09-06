package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/shinpr/galley/internal/provider"
	"github.com/shinpr/galley/internal/runner"
)

// codexLastMessagePath returns the attempt-scoped `--output-last-message`
// capture path Galley requests for Codex executor runs (empty for Claude
// because Claude streams its result directly to stdout JSONL).
func codexLastMessagePath(cli, attemptDir string) string {
	transport, ok := provider.TransportFor(cli)
	if !ok || transport != provider.TransportCodex || attemptDir == "" {
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
	transport, _ := provider.TransportFor(cli)
	if transport == provider.TransportGrok {
		data, err := os.ReadFile(stdoutPath)
		if err != nil {
			data = []byte(stdoutTail)
		}
		return runner.ExtractGrokExecutorResult(data)
	}
	var resultErrs []error
	if transport == provider.TransportCodex && lastMessagePath != "" {
		lastResult, lastErr := runner.ExtractCodexLastMessageFile(lastMessagePath)
		if lastErr == nil {
			return lastResult, nil
		}
		resultErrs = append(resultErrs, fmt.Errorf("codex last-message parse failed: %w", lastErr))
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
	transport, ok := provider.TransportFor(cli)
	if !ok {
		transport = provider.TransportClaude
	}
	switch transport {
	case provider.TransportGrok:
		data, err := os.ReadFile(stdoutPath)
		if err != nil {
			data = []byte(stdoutTail)
		}
		return runner.GrokTerminal(data, runErr)
	case provider.TransportCodex:
		return runner.CodexTerminal(readExecutorStdout(stdoutPath, stdoutTail), runErr)
	case provider.TransportClaude:
		return claudeExecutorTerminal(cli, stdoutPath, stdoutTail, runErr)
	default:
		return claudeExecutorTerminal(cli, stdoutPath, stdoutTail, runErr)
	}
}

// cli is "claude", "glm", or empty (defaulting to claude); GLM shares Claude's
// transport but keeps its own provider identity in evidence.
func claudeExecutorTerminal(cli, stdoutPath, stdoutTail string, runErr error) runner.ExecutorTerminal {
	providerID := cli
	if providerID == "" {
		providerID = "claude"
	}
	return runner.ClaudeTerminal(providerID, readExecutorStdout(stdoutPath, stdoutTail), runErr)
}

func readExecutorStdout(stdoutPath, stdoutTail string) []byte {
	if data, err := os.ReadFile(stdoutPath); err == nil {
		return data
	}
	return []byte(stdoutTail)
}
