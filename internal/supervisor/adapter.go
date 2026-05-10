package supervisor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/shinpr/galley/internal/profile"
	"github.com/shinpr/galley/internal/runner"
	claudeguard "github.com/shinpr/galley/internal/runner/claude_guard_plugin"
	"github.com/shinpr/galley/internal/task"
	"github.com/shinpr/galley/prompts"
	"github.com/shinpr/galley/schemas"
)

// AdapterOptions configures a built-in model supervisor adapter.
type AdapterOptions struct {
	Provider    string
	WorkDir     string
	Timeout     time.Duration
	IdleTimeout time.Duration
	ArtifactDir string
	CodexBin    string
	ClaudeBin   string
}

// AdapterRequest is the JSON request consumed by built-in supervisor adapters.
type AdapterRequest struct {
	Evidence AdapterEvidence `json:"evidence"`
}

// AdapterEvidence is the serializable evidence passed to model supervisors.
type AdapterEvidence struct {
	Task            task.Task           `json:"task"`
	Profiles        profile.Bundle      `json:"profiles"`
	Claude          runner.ClaudeResult `json:"claude"`
	ParseError      string              `json:"parse_error,omitempty"`
	RunError        string              `json:"run_error,omitempty"`
	DiffDirty       bool                `json:"diff_dirty"`
	Diff            string              `json:"diff"`
	DiffError       string              `json:"diff_error,omitempty"`
	Attempt         int                 `json:"attempt"`
	AttemptsLeft    int                 `json:"attempts_left"`
	SourceCWD       string              `json:"source_cwd,omitempty"`
	WorktreeCWD     string              `json:"worktree_cwd,omitempty"`
	PreflightResult any                 `json:"preflight_result,omitempty"`
}

// RunAdapter reviews evidence with a built-in model supervisor.
func RunAdapter(ctx context.Context, opts AdapterOptions, evidence Evidence) (Verdict, error) {
	if opts.Provider == "" {
		return Verdict{}, fmt.Errorf("supervisor provider is required")
	}
	request := NewAdapterRequest(evidence)
	request.Evidence.WorktreeCWD = opts.WorkDir
	payload, err := json.Marshal(request)
	if err != nil {
		return Verdict{}, err
	}
	output, err := RunAdapterPayload(ctx, opts, payload)
	if err != nil {
		return Verdict{}, err
	}
	var verdict Verdict
	if err := json.Unmarshal(output, &verdict); err != nil {
		return Verdict{}, fmt.Errorf("decode %s supervisor verdict: %w", opts.Provider, err)
	}
	if err := ValidateVerdictForEvidence(verdict, evidence); err != nil {
		return Verdict{}, err
	}
	return verdict, nil
}

// RunAdapterPayload runs a built-in model supervisor against a serialized AdapterRequest.
func RunAdapterPayload(ctx context.Context, opts AdapterOptions, request []byte) ([]byte, error) {
	if opts.CodexBin == "" {
		opts.CodexBin = "codex"
	}
	if opts.ClaudeBin == "" {
		opts.ClaudeBin = "claude"
	}
	switch opts.Provider {
	case "codex":
		return runCodexAdapter(ctx, opts, request)
	case "claude":
		return runClaudeAdapter(ctx, opts, request)
	default:
		return nil, fmt.Errorf("supervisor provider must be one of: codex, claude")
	}
}

// NewAdapterRequest converts in-process evidence into the adapter JSON contract.
func NewAdapterRequest(evidence Evidence) AdapterRequest {
	return AdapterRequest{Evidence: AdapterEvidence{
		Task:            evidence.Task,
		Profiles:        evidence.Profiles,
		Claude:          evidence.Claude,
		ParseError:      errorString(evidence.ParseError),
		RunError:        errorString(evidence.RunError),
		DiffDirty:       evidence.DiffDirty,
		Diff:            evidence.Diff,
		DiffError:       errorString(evidence.DiffError),
		Attempt:         evidence.Attempt,
		AttemptsLeft:    evidence.AttemptsLeft,
		SourceCWD:       evidence.Task.Scope.CWD,
		PreflightResult: evidence.PreflightResult,
	}}
}

func runCodexAdapter(ctx context.Context, opts AdapterOptions, request []byte) ([]byte, error) {
	dir, cleanup, err := supervisorArtifactDir(opts.ArtifactDir, "galley-codex-supervisor-*")
	if err != nil {
		return nil, err
	}
	defer cleanup()

	requestPath := filepath.Join(dir, "codex_supervisor_request.json")
	promptPath := filepath.Join(dir, "codex_supervisor_prompt.md")
	schemaPath := filepath.Join(dir, "supervisor-verdict.schema.json")
	outPath := filepath.Join(dir, "codex_supervisor_last_message.json")
	eventsPath := filepath.Join(dir, "codex_supervisor_events.jsonl")
	prompt := prompts.CodexSupervisor() + "\n\n# Evidence JSON\n\n" + string(request)
	if err := writeSupervisorFile(requestPath, request); err != nil {
		return nil, err
	}
	if err := writeSupervisorFile(promptPath, []byte(prompt)); err != nil {
		return nil, err
	}
	if err := writeSupervisorFile(schemaPath, []byte(schemas.SupervisorVerdict)); err != nil {
		return nil, err
	}
	_, err = runner.RunCommand(ctx, runner.Command{
		WorkDir: opts.WorkDir,
		Argv: []string{
			opts.CodexBin,
			"exec",
			"--cd", opts.WorkDir,
			"--sandbox", "workspace-write",
			"--json",
			"--output-schema", schemaPath,
			"--output-last-message", outPath,
			"-",
		},
		Stdin: prompt,
		Env:   runner.RestrictedEnv(),
	}, runner.RunOptions{Timeout: opts.Timeout, IdleTimeout: opts.IdleTimeout, StdoutPath: eventsPath})
	if err != nil {
		return nil, fmt.Errorf("codex supervisor failed: %w", err)
	}
	output, err := os.ReadFile(outPath)
	if err != nil {
		return nil, fmt.Errorf("read codex supervisor verdict: %w", err)
	}
	return output, nil
}

func runClaudeAdapter(ctx context.Context, opts AdapterOptions, request []byte) ([]byte, error) {
	dir, cleanup, err := supervisorArtifactDir(opts.ArtifactDir, "galley-claude-supervisor-*")
	if err != nil {
		return nil, err
	}
	defer cleanup()

	requestPath := filepath.Join(dir, "claude_supervisor_request.json")
	debugPath := filepath.Join(dir, "claude_supervisor_debug.log")
	stdoutPath := filepath.Join(dir, "claude_supervisor_stdout.txt")
	if err := writeSupervisorFile(requestPath, request); err != nil {
		return nil, err
	}
	guardDir, err := claudeguard.Ensure(filepath.Join(dir, "claude-guard-plugin"))
	if err != nil {
		return nil, err
	}
	args := []string{
		opts.ClaudeBin,
		"-p",
		"--no-session-persistence",
		"--permission-mode", "bypassPermissions",
		"--tools", "default",
		"--disallowedTools", "Write,Edit,MultiEdit,NotebookEdit",
		"--output-format", "text",
		"--system-prompt", prompts.ClaudeSupervisor(),
		"--json-schema", schemas.SupervisorVerdict,
		"--plugin-dir", guardDir,
	}
	if opts.ArtifactDir != "" {
		args = append(args, "--debug-file", debugPath)
	}
	_, err = runner.RunCommand(ctx, runner.Command{
		WorkDir: opts.WorkDir,
		Argv:    args,
		Stdin:   string(request),
		Env:     runner.RestrictedEnv("GALLEY_CLAUDE_GUARD_MODE=supervisor"),
	}, runner.RunOptions{Timeout: opts.Timeout, IdleTimeout: opts.IdleTimeout, StdoutPath: stdoutPath})
	if err != nil {
		return nil, fmt.Errorf("claude supervisor failed: %w", err)
	}
	output, err := os.ReadFile(stdoutPath)
	if err != nil {
		return nil, fmt.Errorf("read claude supervisor verdict: %w", err)
	}
	return output, nil
}

func supervisorArtifactDir(dir, pattern string) (string, func(), error) {
	if dir != "" {
		absolute, err := filepath.Abs(dir)
		if err != nil {
			return "", func() {}, fmt.Errorf("resolve supervisor artifact dir: %w", err)
		}
		dir = absolute
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", func() {}, fmt.Errorf("create supervisor artifact dir: %w", err)
		}
		return dir, func() {}, nil
	}
	tmp, err := os.MkdirTemp("", pattern)
	if err != nil {
		return "", func() {}, err
	}
	return tmp, func() { _ = os.RemoveAll(tmp) }, nil
}

func writeSupervisorFile(path string, body []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
