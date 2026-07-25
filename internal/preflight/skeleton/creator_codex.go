// Package skeleton contains Codex provider integration for the acceptance
// skeleton creator, kept separate from the Claude creator path so provider
// routing, runner integration, and manifest capture can be reviewed
// independently.
package skeleton

import (
	"os"

	"github.com/shinpr/galley/internal/proc"
	"github.com/shinpr/galley/internal/runner"
	"github.com/shinpr/galley/prompts"
	"github.com/shinpr/galley/schemas"
)

// buildCodexCreatorCommandPlan builds the Codex provider creator command plan.
//
// runner.CodexFromTask carries the task implementation executor backend
// configuration (model, effort, sandbox, prompt mode, budget) so the creator
// run and the implementation attempt share the same backend settings.
// The acceptance skeleton creator prompt and manifest schema are supplied
// explicitly, and RunDir is used as the attempt directory so the Codex runner
// materializes the `--output-schema` file and requests the
// `--output-last-message` capture file alongside the other preflight creator
// evidence.
func buildCodexCreatorCommandPlan(opts Options, payload []byte) (proc.Command, *preflightErr) {
	codexOpts := runner.CodexFromTask(opts.Task)
	codexOpts.Bin = opts.CodexBin
	if codexOpts.Bin == "" {
		codexOpts.Bin = "codex"
	}
	codexOpts.WorkDir = opts.WorkDir
	codexOpts.Prompt = string(payload)
	codexOpts.SystemPrompt = prompts.AcceptanceSkeletonCreatorCodex()
	codexOpts.JSONSchema = schemas.AcceptanceSkeletonManifest
	codexOpts.AttemptDir = opts.RunDir

	commandPlan, err := runner.CodexCommandPlan(codexOpts)
	if err != nil {
		return proc.Command{}, creatorErr("plan built-in creator: %v", err)
	}
	return commandPlan, nil
}

// extractCodexCreatorManifest resolves the creator manifest for a Codex creator
// run. The Codex CLI writes the final assistant message verbatim to the
// attempt-scoped `--output-last-message` capture file, so that file is the
// canonical surface: a successful Codex creator run produces a manifest JSON
// object there even when the JSONL stdout stream is truncated or empty. The
// JSON event stream is only a best-effort fallback.
func extractCodexCreatorManifest(lastMessagePath, stdoutTail, stdoutPath string) (creatorManifest, error) {
	if data, err := os.ReadFile(lastMessagePath); err == nil {
		if manifest, ok := parseCodexCreatorManifestData(string(data)); ok {
			return manifest, nil
		}
	}
	// Fallback: scan the Codex JSON event stream when the capture file is
	// missing or did not contain a manifest.
	return extractCreatorManifestFromStdout(stdoutTail, stdoutPath)
}

// parseCodexCreatorManifestData parses the manifest from a Codex
// `--output-last-message` capture file. The captured final message is normally
// a single JSON object, but it may also be prose-wrapped or a JSON event stream
// containing the manifest.
func parseCodexCreatorManifestData(data string) (creatorManifest, bool) {
	if manifest, ok := parseCreatorManifestText(data); ok {
		return manifest, true
	}
	if manifest, err := extractCreatorManifest(data); err == nil {
		return manifest, true
	}
	return creatorManifest{}, false
}
