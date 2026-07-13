package setup

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/shinpr/galley/internal/jsonio"
	"github.com/shinpr/galley/internal/runartifact"
	"github.com/shinpr/galley/internal/runner"
	claudeguard "github.com/shinpr/galley/internal/runner/claude_guard_plugin"
	"github.com/shinpr/galley/internal/task"
	"github.com/shinpr/galley/prompts"
	"github.com/shinpr/galley/schemas"
)

func marshalSetupExecutorRequest(opts Options, signals []string) ([]byte, error) {
	request := map[string]any{
		"task": map[string]any{
			"id":                  opts.Task.ID,
			"mode":                opts.Task.Mode,
			"goal":                opts.Task.Goal,
			"acceptance_criteria": opts.Task.AcceptanceCriteria,
			"scope":               opts.Task.Scope,
			"execution_policy":    opts.Task.ExecutionPolicy,
			"executor":            opts.Task.Executor,
			"preflight":           opts.Task.Preflight,
		},
		"environment":        opts.Profiles.Environment,
		"quality":            opts.Profiles.Quality,
		"repository_signals": signals,
		"worktree":           opts.WorkDir,
	}
	return json.MarshalIndent(request, "", " ")
}

func BuildExecutorCommandPlan(opts Options, payload []byte) (runner.Command, string, error) {
	provider := task.ExecutorProvider(opts.Task)
	switch provider {
	case "codex":
		cmd, err := buildCodexSetupExecutorCommandPlan(opts, payload)
		return cmd, provider, err
	case "grok":
		cmd, err := buildGrokSetupExecutorCommandPlan(opts, payload)
		return cmd, provider, err
	default:
		cmd, err := buildClaudeSetupExecutorCommandPlan(opts, payload)
		if err != nil {
			return runner.Command{}, "claude", err
		}
		// glm redirects the setup executor to GLM's endpoint like any executor role.
		if opts.Task.Executor.CLI == "glm" {
			token, terr := runner.ResolveGLMToken(opts.GLMAuthToken)
			if terr != nil {
				return runner.Command{}, "claude", terr
			}
			runner.RedirectClaudeToGLM(&cmd, token)
		}
		return cmd, "claude", nil
	}
}

func buildGrokSetupExecutorCommandPlan(opts Options, payload []byte) (runner.Command, error) {
	grokOpts := runner.GrokFromTask(opts.Task)
	grokOpts.Bin = opts.GrokBin
	grokOpts.WorkDir = opts.WorkDir
	grokOpts.Prompt = string(payload)
	grokOpts.SystemPrompt = prompts.SetupExecutorGrok()
	grokOpts.JSONSchema = schemas.SetupResult
	grokOpts.PermissionMode = "bypassPermissions"
	grokOpts.AttemptDir = opts.RunDir
	grokOpts.PromptFilename = "grok.setup.prompt.md"
	plan, err := runner.GrokCommandPlan(grokOpts)
	if err != nil {
		return runner.Command{}, fmt.Errorf("plan setup executor: %w", err)
	}
	return plan, nil
}

func buildClaudeSetupExecutorCommandPlan(opts Options, payload []byte) (runner.Command, error) {
	bin := opts.ClaudeBin
	if bin == "" {
		bin = "claude"
	}
	guardDir, err := claudeguard.Ensure(filepath.Join(opts.RunDir, "claude-guard-plugin"))
	if err != nil {
		return runner.Command{}, fmt.Errorf("prepare setup guard plugin: %w", err)
	}
	guardDir, err = filepath.Abs(guardDir)
	if err != nil {
		return runner.Command{}, fmt.Errorf("resolve setup guard plugin: %w", err)
	}
	commandPlan, err := runner.ClaudeCommandPlan(runner.ClaudeOptions{
		Bin:            bin,
		Model:          opts.Task.Executor.Model,
		Effort:         opts.Task.Executor.Effort,
		WorkDir:        opts.WorkDir,
		Prompt:         string(payload),
		SystemPrompt:   prompts.SetupExecutorClaude(),
		JSONSchema:     schemas.SetupResult,
		PermissionMode: "bypassPermissions",
		PluginDirs:     []string{guardDir},
		AttemptDir:     opts.RunDir,
	})
	if err != nil {
		return runner.Command{}, fmt.Errorf("plan setup executor: %w", err)
	}
	commandPlan.EnvAppend = []string{"GALLEY_CLAUDE_GUARD_MODE=setup_executor"}
	return commandPlan, nil
}

func buildCodexSetupExecutorCommandPlan(opts Options, payload []byte) (runner.Command, error) {
	codexOpts := runner.CodexFromTask(opts.Task)
	codexOpts.Bin = opts.CodexBin
	if codexOpts.Bin == "" {
		codexOpts.Bin = "codex"
	}
	codexOpts.WorkDir = opts.WorkDir
	codexOpts.Prompt = string(payload)
	codexOpts.SystemPrompt = prompts.SetupExecutorCodex()
	schema, err := runner.CodexCompatibleOutputSchema(schemas.SetupResult)
	if err != nil {
		return runner.Command{}, fmt.Errorf("prepare setup executor schema: %w", err)
	}
	codexOpts.JSONSchema = schema
	codexOpts.AttemptDir = opts.RunDir

	commandPlan, err := runner.CodexCommandPlan(codexOpts)
	if err != nil {
		return runner.Command{}, fmt.Errorf("plan setup executor: %w", err)
	}
	return commandPlan, nil
}

func writeSetupExecutorCommandPlan(runDir string, commandPlan runner.Command) error {
	planPath := runartifact.Path(runDir, runartifact.SetupExecutorPlanFilename)
	auditPlan := commandPlan
	return jsonio.Write(planPath, auditPlan)
}

func RunExecutorCommand(ctx context.Context, opts Options, commandPlan runner.Command) (runner.RunResult, error) {
	stdoutPath := runartifact.Path(opts.RunDir, runartifact.SetupExecutorStdoutFilename)
	stderrPath := runartifact.Path(opts.RunDir, runartifact.SetupExecutorStderrFilename)
	return runner.RunCommand(ctx, commandPlan, runner.RunOptions{
		Timeout:    setupCommandTimeout(opts.Task),
		StdoutPath: stdoutPath,
		StderrPath: stderrPath,
		TailBytes:  16384,
	})
}
