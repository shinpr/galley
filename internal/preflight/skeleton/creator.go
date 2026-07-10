package skeleton

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shinpr/galley/internal/jsonio"
	"github.com/shinpr/galley/internal/runartifact"
	"github.com/shinpr/galley/internal/runner"
	claudeguard "github.com/shinpr/galley/internal/runner/claude_guard_plugin"
	"github.com/shinpr/galley/internal/task"
	"github.com/shinpr/galley/prompts"
	"github.com/shinpr/galley/schemas"
)

// noSkeletonDeclaration is the internal shape for a creator-reported AC that
// intentionally has no executable skeleton.
type noSkeletonDeclaration struct {
	ACID   string
	Reason string
}

// creatorManifest is the JSON document returned by the built-in skeleton
// creator and persisted as run evidence.
type creatorManifest struct {
	Outputs []struct {
		ACID                   string `json:"ac_id"`
		Path                   string `json:"path"`
		Kind                   string `json:"kind"`
		Purpose                string `json:"purpose"`
		Satisfies              string `json:"satisfies"`
		IntegrationPoint       string `json:"integration_point"`
		ImplementationRequired bool   `json:"implementation_required"`
	} `json:"outputs"`
	NoSkeletons []struct {
		ACID   string `json:"ac_id"`
		Reason string `json:"reason"`
	} `json:"no_skeletons"`
}

// resolveSkeletonDeclarations returns the skeleton outputs and no_skeletons
// entries for the preflight stage. It always uses the built-in test creator
// because the core value is reasoning from natural-language task context and
// ACs into concrete test skeletons.
func resolveSkeletonDeclarations(ctx context.Context, opts Options) ([]task.AcceptanceSkeletonOutputDef, []noSkeletonDeclaration, *preflightErr) {
	return runBuiltinSkeletonCreator(ctx, opts)
}

func runBuiltinSkeletonCreator(ctx context.Context, opts Options) ([]task.AcceptanceSkeletonOutputDef, []noSkeletonDeclaration, *preflightErr) {
	allowed, _, _ := EffectivePreflightPaths(opts.Task)
	payload, perr := marshalBuiltinCreatorRequest(opts, allowed)
	if perr != nil {
		return nil, nil, perr
	}
	commandPlan, perr := buildBuiltinCreatorCommandPlan(opts, payload)
	if perr != nil {
		return nil, nil, perr
	}
	if perr := writeBuiltinCreatorCommandPlan(opts.RunDir, commandPlan); perr != nil {
		return nil, nil, perr
	}
	manifest, perr := runBuiltinCreatorCommand(ctx, opts, commandPlan)
	if perr != nil {
		return nil, nil, perr
	}
	if err := jsonio.Write(runartifact.Path(opts.RunDir, runartifact.PreflightCreatorManifestFilename), manifest); err != nil {
		return nil, nil, creatorErr("write creator manifest: %v", err)
	}
	decls, noSkel := creatorManifestToDeclarations(manifest)
	return decls, noSkel, nil
}

func marshalBuiltinCreatorRequest(opts Options, allowed []string) ([]byte, *preflightErr) {
	request := map[string]any{
		"task":            opts.Task,
		"allowed_paths":   allowed,
		"profiles":        opts.Profiles,
		"reference_files": opts.Task.Files,
	}
	payload, err := json.MarshalIndent(request, "", " ")
	if err != nil {
		return nil, creatorErr("encode creator request: %v", err)
	}
	return payload, nil
}

// buildBuiltinCreatorCommandPlan routes command-plan construction to the
// provider selected from the task implementation executor backend. The
// Codex path runs through the Codex command planner; the Claude path keeps the
// existing Claude creator behavior including the JSON guard plugin.
func buildBuiltinCreatorCommandPlan(opts Options, payload []byte) (runner.Command, *preflightErr) {
	if task.ExecutorProvider(opts.Task) == "codex" {
		return buildCodexCreatorCommandPlan(opts, payload)
	}
	cmd, perr := buildClaudeCreatorCommandPlan(opts, payload)
	if perr != nil {
		return runner.Command{}, perr
	}
	// glm redirects the skeleton creator to GLM's endpoint like any executor role.
	if opts.Task.Executor.CLI == "glm" {
		token, terr := runner.ResolveGLMToken(opts.GLMAuthToken)
		if terr != nil {
			return runner.Command{}, creatorErr("%v", terr)
		}
		runner.RedirectClaudeToGLM(&cmd, token)
	}
	return cmd, nil
}

// buildClaudeCreatorCommandPlan builds the Claude provider creator command
// plan. Task executor model and effort are propagated so the creator run uses
// the same executor backend configuration as the implementation attempt.
func buildClaudeCreatorCommandPlan(opts Options, payload []byte) (runner.Command, *preflightErr) {
	bin := opts.ClaudeBin
	if bin == "" {
		bin = "claude"
	}
	guardDir, err := claudeguard.Ensure(filepath.Join(opts.RunDir, "claude-guard-plugin"))
	if err != nil {
		return runner.Command{}, creatorErr("prepare creator JSON guard: %v", err)
	}
	guardDir, err = filepath.Abs(guardDir)
	if err != nil {
		return runner.Command{}, creatorErr("resolve creator JSON guard: %v", err)
	}
	commandPlan, err := runner.ClaudeCommandPlan(runner.ClaudeOptions{
		Bin:            bin,
		Model:          opts.Task.Executor.Model,
		Effort:         opts.Task.Executor.Effort,
		WorkDir:        opts.WorkDir,
		Prompt:         string(payload),
		SystemPrompt:   prompts.AcceptanceSkeletonCreator(),
		JSONSchema:     schemas.AcceptanceSkeletonManifest,
		PromptMode:     "replace",
		PermissionMode: "bypassPermissions",
		PluginDirs:     []string{guardDir},
		AttemptDir:     opts.RunDir,
	})
	if err != nil {
		return runner.Command{}, creatorErr("plan built-in creator: %v", err)
	}
	commandPlan.EnvAppend = []string{"GALLEY_CLAUDE_GUARD_MODE=acceptance_skeleton_creator"}
	return commandPlan, nil
}

func writeBuiltinCreatorCommandPlan(runDir string, commandPlan runner.Command) *preflightErr {
	planPath := runartifact.Path(runDir, runartifact.PreflightCreatorPlanFilename)
	auditPlan := commandPlan
	if err := jsonio.Write(planPath, auditPlan); err != nil {
		return creatorErr("write creator command plan: %v", err)
	}
	return nil
}

func runBuiltinCreatorCommand(ctx context.Context, opts Options, commandPlan runner.Command) (creatorManifest, *preflightErr) {
	stdoutPath := filepath.Join(opts.RunDir, "preflight_creator.stdout.jsonl")
	stderrPath := filepath.Join(opts.RunDir, "preflight_creator.stderr.log")
	out, err := runner.RunCommand(ctx, commandPlan, runner.RunOptions{
		Timeout:    time.Duration(opts.Task.ExecutionPolicy.TimeoutMS) * time.Millisecond,
		StdoutPath: stdoutPath,
		StderrPath: stderrPath,
		TailBytes:  8192,
	})
	if err != nil {
		return creatorManifest{}, &preflightErr{
			phase:   "acceptance_skeleton_creator",
			message: fmt.Sprintf("built-in creator exited %d: %s", out.ExitCode, strings.TrimSpace(out.Stderr)),
		}
	}
	manifest, err := resolveCreatorManifest(opts, out.Stdout, stdoutPath)
	if err != nil {
		return creatorManifest{}, creatorErr("built-in creator did not return a valid manifest: %v", err)
	}
	return manifest, nil
}

// resolveCreatorManifest parses the creator manifest from the provider's
// canonical output surface. Codex writes its structured final message to the
// attempt-scoped `--output-last-message` capture file, so the Codex path reads
// that file first and only falls back to the JSON event stream. Claude streams
// the manifest directly on stdout.
func resolveCreatorManifest(opts Options, stdoutTail, stdoutPath string) (creatorManifest, error) {
	if task.ExecutorProvider(opts.Task) == "codex" {
		lastMessagePath := filepath.Join(opts.RunDir, runner.CodexLastMessageFilename)
		return extractCodexCreatorManifest(lastMessagePath, stdoutTail, stdoutPath)
	}
	return extractClaudeCreatorManifest(stdoutTail, stdoutPath)
}

// extractCreatorManifestFromStdout parses the manifest from a creator stdout
// JSONL stream, with the persisted stdout file as a fallback when the captured
// tail did not contain the manifest.
func extractCreatorManifestFromStdout(stdoutTail, stdoutPath string) (creatorManifest, error) {
	manifest, err := extractCreatorManifest(stdoutTail)
	if err != nil {
		if data, readErr := os.ReadFile(stdoutPath); readErr == nil {
			manifest, err = extractCreatorManifest(string(data))
		}
	}
	return manifest, err
}

// extractClaudeCreatorManifest parses the manifest from the Claude creator
// stdout JSONL stream.
func extractClaudeCreatorManifest(stdoutTail, stdoutPath string) (creatorManifest, error) {
	return extractCreatorManifestFromStdout(stdoutTail, stdoutPath)
}

func creatorManifestToDeclarations(manifest creatorManifest) ([]task.AcceptanceSkeletonOutputDef, []noSkeletonDeclaration) {
	decls := make([]task.AcceptanceSkeletonOutputDef, 0, len(manifest.Outputs))
	for _, o := range manifest.Outputs {
		decls = append(decls, task.AcceptanceSkeletonOutputDef{
			ACID:                   o.ACID,
			Path:                   o.Path,
			Kind:                   o.Kind,
			Purpose:                o.Purpose,
			Satisfies:              o.Satisfies,
			IntegrationPoint:       o.IntegrationPoint,
			ImplementationRequired: o.ImplementationRequired,
		})
	}
	noSkel := make([]noSkeletonDeclaration, 0, len(manifest.NoSkeletons))
	for _, n := range manifest.NoSkeletons {
		noSkel = append(noSkel, noSkeletonDeclaration{ACID: n.ACID, Reason: n.Reason})
	}
	return decls, noSkel
}

func creatorErr(format string, args ...any) *preflightErr {
	return &preflightErr{phase: "acceptance_skeleton_creator", message: fmt.Sprintf(format, args...)}
}

func extractCreatorManifest(stdout string) (creatorManifest, error) {
	var firstErr error
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if manifest, ok, err := parseCreatorManifestLine(line); ok {
			return manifest, err
		} else if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if firstErr != nil {
		return creatorManifest{}, firstErr
	}
	return creatorManifest{}, fmt.Errorf("manifest JSON not found")
}

func parseCreatorManifestLine(line string) (creatorManifest, bool, error) {
	if manifest, ok := parseCreatorManifestText(line); ok {
		return manifest, true, nil
	}
	var event map[string]json.RawMessage
	if err := json.Unmarshal([]byte(line), &event); err != nil {
		return creatorManifest{}, false, nil
	}
	for _, key := range []string{"result", "response", "message"} {
		raw, ok := event[key]
		if !ok {
			continue
		}
		var text string
		if err := json.Unmarshal(raw, &text); err == nil {
			if manifest, ok := parseCreatorManifestText(text); ok {
				return manifest, true, nil
			}
		}
		if manifest, ok := parseCreatorManifestRaw(raw); ok {
			return manifest, true, nil
		}
	}
	return creatorManifest{}, false, nil
}

func parseCreatorManifestText(text string) (creatorManifest, bool) {
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start < 0 || end < start {
		return creatorManifest{}, false
	}
	return parseCreatorManifestRaw([]byte(text[start : end+1]))
}

func parseCreatorManifestRaw(data []byte) (creatorManifest, bool) {
	var manifest creatorManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return creatorManifest{}, false
	}
	if manifest.Outputs == nil || manifest.NoSkeletons == nil {
		return creatorManifest{}, false
	}
	return manifest, true
}
