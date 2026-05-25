// Package daemon — Setup Executor Preflight stage.
//
// SetupExecutorPreflight prepares a fresh task worktree before the acceptance
// skeleton preflight and before the implementation executor. It runs after
// inputfiles.Prepare so the prepared worktree (with input files staged) is the
// state the setup acts on, and before AcceptanceSkeletonPreflight so setup
// readiness is verified independently of any task-specific skeletons (AC2,
// AC10).
//
// The daemon always delegates setup execution to the setup executor (Claude or
// Codex per task.executor.cli). environment.setup.commands, when present, is
// passed as a prior plan for the setup executor to try, diagnose, and repair in
// the same model context. On success the daemon persists
// runs/<run-id>/setup_result.json (AC8) and, when the successful plan differs
// from the resolved environment profile, atomically rewrites the repository
// environment.yaml setup field (AC7) and records the change in
// runs/<run-id>/environment_update.json so a subsequent task reuses the learned
// setup without rediscovery.
//
// On failure the stage classifies the error with phase "setup" and kind
// "setup_failed" (AC9) and writes the setup_result.json failure record with
// attempted commands, command source, inspected files, stdout/stderr excerpts,
// and repair guidance so the operator can fix environment.setup before the next
// attempt.
package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shinpr/galley/internal/profile"
	"github.com/shinpr/galley/internal/runner"
	claudeguard "github.com/shinpr/galley/internal/runner/claude_guard_plugin"
	"github.com/shinpr/galley/internal/task"
	"github.com/shinpr/galley/prompts"
	"github.com/shinpr/galley/schemas"
)

// SetupResultStatus enumerates the setup phase outcomes Galley records.
const (
	SetupStatusReady  = "ready"
	SetupStatusFailed = "failed"
)

// Setup-phase classification constants used by daemon failure routing (AC9).
const (
	SetupPhase      = "setup"
	SetupFailedKind = "setup_failed"
)

// Source values recorded in SetupCommandAttempt.Source so reviewers can
// distinguish prior-plan commands from learned commands (AC6).
const (
	SetupSourceEnvironmentSetup    = "environment_setup"
	SetupSourceEnvironmentCommands = "environment_commands"
	SetupSourceDiscovered          = "discovered"
	SetupSourceReadinessCheck      = "readiness_check" // legacy evidence value
)

// SetupResult is the runtime source-of-truth output of the setup executor
// preflight. It is serialized to runs/<run-id>/setup_result.json (AC8).
type SetupResult struct {
	Status             string                 `json:"status" yaml:"status"`
	Commands           []SetupCommandAttempt  `json:"commands" yaml:"commands"`
	SuccessfulCommands []profile.SetupCommand `json:"successful_commands,omitempty" yaml:"successful_commands,omitempty"`
	InspectedFiles     []string               `json:"inspected_files,omitempty" yaml:"inspected_files,omitempty"`
	ReadinessEvidence  string                 `json:"readiness_evidence,omitempty" yaml:"readiness_evidence,omitempty"`
	RepairGuidance     string                 `json:"repair_guidance,omitempty" yaml:"repair_guidance,omitempty"`
	Error              string                 `json:"error,omitempty" yaml:"error,omitempty"`
	Provider           string                 `json:"provider,omitempty" yaml:"provider,omitempty"`
	Source             string                 `json:"source,omitempty" yaml:"source,omitempty"`
}

// SetupCommandAttempt is one command the setup executor attempted. Stdout/stderr
// are truncated excerpts; the full subprocess output remains in the setup
// executor logs under the run directory.
type SetupCommandAttempt struct {
	Run           string `json:"run" yaml:"run"`
	Why           string `json:"why,omitempty" yaml:"why,omitempty"`
	Source        string `json:"source" yaml:"source"`
	ExitCode      int    `json:"exit_code" yaml:"exit_code"`
	StdoutExcerpt string `json:"stdout_excerpt,omitempty" yaml:"stdout_excerpt,omitempty"`
	StderrExcerpt string `json:"stderr_excerpt,omitempty" yaml:"stderr_excerpt,omitempty"`
}

// SetupEnvironmentUpdate is persisted to runs/<run-id>/environment_update.json
// when the daemon rewrites the repository environment.yaml setup field (AC7,
// AC8).
type SetupEnvironmentUpdate struct {
	ProfilePath string             `json:"profile_path"`
	Changed     bool               `json:"changed"`
	Before      *profile.SetupPlan `json:"before,omitempty"`
	After       profile.SetupPlan  `json:"after"`
	Diff        string             `json:"diff,omitempty"`
	Reason      string             `json:"reason"`
	UpdatedAt   string             `json:"updated_at"`
}

const (
	maxSetupResultCommands      = 50
	maxSetupResultFiles         = 100
	maxSetupResultExcerptLength = 400
	maxSetupResultTextLength    = 2048
)

// SetupExecutorPreflightOptions configures one preflight invocation. The
// EnvironmentProfilePath is required when the daemon should persist a learned
// setup plan back to the repository environment profile (AC7).
type SetupExecutorPreflightOptions struct {
	Task                   task.Task
	WorkDir                string
	RunDir                 string
	Profiles               profile.Bundle
	ClaudeBin              string
	CodexBin               string
	EnvironmentProfilePath string
	// RepositorySignals is an optional list of repository setup signal paths
	// (manifests, lockfiles, setup docs) that Galley surfaces to the setup
	// executor. The daemon discovers these from the worktree before invoking the
	// executor so the prompt context is grounded in real repository evidence.
	RepositorySignals []string
}

// setupExecutorRunner is the package-level dispatch hook for the setup
// executor. Tests override it to drive the preflight without spawning a real
// claude or codex subprocess. The default runs the real provider via
// runSetupExecutor.
var setupExecutorRunner = runSetupExecutor

// SetupExecutorPreflight orchestrates the setup phase. It returns the result
// (nil when no setup work was needed and the daemon should skip the stage),
// an optional environment update record, and an error. The error is non-nil
// only on hard preflight failures (Galley-side I/O, contract violations, a
// setup executor that failed to produce a ready worktree, or a learned-plan
// persistence failure that means AC7 cannot be honored for the next task).
func SetupExecutorPreflight(ctx context.Context, opts SetupExecutorPreflightOptions) (*SetupResult, *SetupEnvironmentUpdate, error) {
	if opts.RunDir == "" {
		return nil, nil, fmt.Errorf("setup preflight: run dir is required")
	}
	if opts.WorkDir == "" {
		return nil, nil, fmt.Errorf("setup preflight: work dir is required")
	}
	if opts.Profiles.Environment == nil {
		// Without an environment profile there is no setup contract to enforce
		// and no commands map to learn from. Skip the stage so existing tasks
		// without an environment profile keep the prior daemon behavior.
		return nil, nil, nil
	}

	env := opts.Profiles.Environment
	res, err := setupExecutorRunner(ctx, opts)
	if writeErr := WriteSetupResult(opts.RunDir, res); writeErr != nil && err == nil {
		err = writeErr
	}
	if err != nil {
		return res, nil, err
	}
	if res == nil || res.Status != SetupStatusReady {
		return res, nil, fmt.Errorf("setup executor did not make the worktree ready")
	}
	// Discovery reported ready: enforce the learned-plan contract before
	// persistence so a ready+empty-successful_commands response cannot silently
	// leave environment.yaml unchanged (AC7 invariant).
	if vErr := enforceLearnedSetupPlanContract(res); vErr != nil {
		applySetupContractViolation(res, vErr)
		_ = WriteSetupResult(opts.RunDir, res)
		return res, nil, fmt.Errorf("setup phase failed: %w", vErr)
	}
	// On success, persist the learned plan back to the environment profile when
	// the resolved profile lacked a setup field (AC7) so subsequent tasks reuse
	// it without rediscovery. A failure to persist is treated as a setup-phase
	// failure: AC7 explicitly requires that subsequent tasks see the learned
	// plan without rediscovery, so silently swallowing the rewrite error would
	// re-cost discovery every run.
	update, perr := persistLearnedSetupPlan(opts, env, res)
	if perr != nil {
		recordSetupProfileUpdateFailure(opts.RunDir, perr)
		// Promote the persistence failure into the setup result so
		// setup_result.json carries the same failure facts as the rest of the
		// run evidence (AC8).
		if res != nil {
			res.Status = SetupStatusFailed
			if res.Error == "" {
				res.Error = "learned setup plan persistence failed: " + perr.Error()
			}
			if res.RepairGuidance == "" {
				res.RepairGuidance = "Inspect environment_update.json, fix the environment.yaml under runs/<run-id>/setup_result.json, and requeue the task."
			}
			_ = WriteSetupResult(opts.RunDir, res)
		}
		return res, nil, fmt.Errorf("setup phase failed: persist learned setup plan: %w", perr)
	}
	if update != nil {
		if err := WriteSetupEnvironmentUpdate(opts.RunDir, update); err != nil {
			return res, nil, err
		}
	}
	return res, update, nil
}

// runSetupExecutor dispatches the setup executor (Claude or Codex per
// task.executor.cli) to attempt to make the worktree ready.
func runSetupExecutor(ctx context.Context, opts SetupExecutorPreflightOptions) (*SetupResult, error) {
	signals := opts.RepositorySignals
	if signals == nil {
		signals = discoverRepositorySignals(opts.WorkDir)
	}
	payload, perr := marshalSetupExecutorRequest(opts, signals)
	if perr != nil {
		return setupExecutorFailureResult("plan setup executor request: "+perr.Error(), "", "", 0, "", "", signals), perr
	}
	commandPlan, provider, perr := buildSetupExecutorCommandPlan(opts, payload)
	if perr != nil {
		return setupExecutorFailureResult("plan setup executor command: "+perr.Error(), provider, "", 0, "", "", signals), perr
	}
	if err := writeSetupExecutorCommandPlan(opts.RunDir, commandPlan); err != nil {
		return setupExecutorFailureResult("write setup executor command plan: "+err.Error(), provider, "", 0, "", "", signals), err
	}
	out, runErr := runSetupExecutorCommand(ctx, opts, commandPlan)
	executorRun := fmt.Sprintf("<setup_executor:%s>", provider)
	parsed, parseErr := resolveSetupExecutorResult(opts, out.Stdout)
	if parseErr != nil {
		message := "setup executor did not return a valid result: " + parseErr.Error()
		if runErr != nil {
			message = fmt.Sprintf("setup executor exited %d: %s", out.ExitCode, truncateExcerpt(out.Stderr))
		}
		failure := setupExecutorFailureResult(message, provider, executorRun, out.ExitCode, out.Stdout, out.Stderr, signals)
		return failure, fmt.Errorf("setup executor failed: %v", parseErr)
	}
	parsed.Provider = provider
	if parsed.Status == "" {
		parsed.Status = SetupStatusFailed
	}
	if runErr != nil && parsed.Status == SetupStatusReady {
		// Defensive: the runner reported a non-zero exit but the executor
		// declared ready. Trust the runner — readiness without exit 0 is not
		// trustworthy.
		parsed.Status = SetupStatusFailed
		if parsed.Error == "" {
			parsed.Error = fmt.Sprintf("setup executor process exited non-zero: %v", runErr)
		}
	}
	return parsed, nil
}

// setupExecutorFailureResult builds a structured setup failure record with
// enough evidence (AC9) for an operator to repair environment.setup. When the
// failure surfaced from an actual setup executor invocation the caller passes
// the executor run identifier, exit code, stdout/stderr, and inspected files.
// When the failure occurred before the executor could run (request marshaling,
// command plan construction) the executor-specific arguments may be zero
// values and only the message is recorded.
func setupExecutorFailureResult(message, provider, executorRun string, exitCode int, stdout, stderr string, inspected []string) *SetupResult {
	res := &SetupResult{
		Status:         SetupStatusFailed,
		Commands:       []SetupCommandAttempt{},
		Error:          message,
		Provider:       provider,
		Source:         SetupSourceDiscovered,
		InspectedFiles: append([]string{}, inspected...),
		RepairGuidance: "Inspect runs/<run-id>/setup_executor.stderr.log and runs/<run-id>/setup_executor.stdout.jsonl; ensure environment.setup or environment.commands provides a working plan, then requeue.",
	}
	if executorRun != "" {
		res.Commands = append(res.Commands, SetupCommandAttempt{
			Run:           executorRun,
			Why:           "setup executor invocation",
			Source:        SetupSourceDiscovered,
			ExitCode:      exitCode,
			StdoutExcerpt: truncateExcerpt(stdout),
			StderrExcerpt: truncateExcerpt(stderr),
		})
	}
	return res
}

// enforceLearnedSetupPlanContract validates that a setup executor that returned
// status=ready also returned a non-empty successful_commands plan. The daemon
// does not execute environment.setup.commands itself, so a ready response
// without the final successful plan would leave the next task without the setup
// executor's repaired command sequence.
func enforceLearnedSetupPlanContract(res *SetupResult) error {
	if res == nil || res.Status != SetupStatusReady {
		return nil
	}
	if len(res.SuccessfulCommands) > 0 {
		return nil
	}
	return fmt.Errorf("setup executor returned status=ready with no successful_commands; cannot learn a setup plan to persist to environment.yaml")
}

// applySetupContractViolation downgrades a result that violated the
// learned-plan contract from ready to failed and ensures repair guidance is
// present so the saved setup_result.json carries enough detail for the
// operator to fix environment.commands or author environment.setup before
// requeuing.
func applySetupContractViolation(res *SetupResult, err error) {
	if res == nil || err == nil {
		return
	}
	res.Status = SetupStatusFailed
	if res.Error == "" {
		res.Error = err.Error()
	}
	if res.RepairGuidance == "" {
		res.RepairGuidance = "Return the ordered successful_commands the setup executor ran, then requeue. environment.yaml was intentionally left unchanged to avoid silently dropping a stale or unknown setup plan."
	}
}

// persistLearnedSetupPlan writes the successful setup plan back to the
// repository environment profile when the resolved profile lacked a setup
// field or the learned plan differs from the existing one. The rewrite is
// atomic and validates the result before returning (AC7).
func persistLearnedSetupPlan(opts SetupExecutorPreflightOptions, env *profile.Environment, res *SetupResult) (*SetupEnvironmentUpdate, error) {
	if res == nil || res.Status != SetupStatusReady {
		return nil, nil
	}
	if len(res.SuccessfulCommands) == 0 {
		return nil, nil
	}
	if opts.EnvironmentProfilePath == "" {
		return nil, nil
	}
	plan := profile.SetupPlan{Commands: append([]profile.SetupCommand{}, res.SuccessfulCommands...)}
	if env.Setup != nil && setupPlansEqual(*env.Setup, plan) {
		return nil, nil
	}
	prior, err := profile.UpdateEnvironmentSetup(opts.EnvironmentProfilePath, plan)
	if err != nil {
		return nil, err
	}
	reason := "no setup field; persisted learned plan"
	if prior != nil {
		reason = "learned plan differs from prior setup; updated"
	}
	return &SetupEnvironmentUpdate{
		ProfilePath: opts.EnvironmentProfilePath,
		Changed:     true,
		Before:      prior,
		After:       plan,
		Diff:        setupPlanDiff(prior, plan),
		Reason:      reason,
		UpdatedAt:   time.Now().UTC().Format(time.RFC3339Nano),
	}, nil
}

func setupPlanDiff(before *profile.SetupPlan, after profile.SetupPlan) string {
	var b strings.Builder
	b.WriteString("environment.setup.commands\n")
	if before == nil || len(before.Commands) == 0 {
		b.WriteString("- <absent>\n")
	} else {
		for _, cmd := range before.Commands {
			fmt.Fprintf(&b, "- run: %q", cmd.Run)
			if cmd.Why != "" {
				fmt.Fprintf(&b, " why: %q", cmd.Why)
			}
			b.WriteString("\n")
		}
	}
	for _, cmd := range after.Commands {
		fmt.Fprintf(&b, "+ run: %q", cmd.Run)
		if cmd.Why != "" {
			fmt.Fprintf(&b, " why: %q", cmd.Why)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func setupPlansEqual(a, b profile.SetupPlan) bool {
	if len(a.Commands) != len(b.Commands) {
		return false
	}
	for i := range a.Commands {
		if strings.TrimSpace(a.Commands[i].Run) != strings.TrimSpace(b.Commands[i].Run) {
			return false
		}
	}
	return true
}

func recordSetupProfileUpdateFailure(runDir string, err error) {
	payload := map[string]any{
		"changed": false,
		"error":   err.Error(),
	}
	_ = writeJSON(filepath.Join(runDir, "environment_update.json"), payload)
}

// WriteSetupResult persists the source-of-truth setup_result.json (AC8).
func WriteSetupResult(runDir string, res *SetupResult) error {
	if runDir == "" {
		return fmt.Errorf("run dir is required for setup result")
	}
	if res == nil {
		return nil
	}
	normalizeSetupResult(res)
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		return fmt.Errorf("create setup run dir: %w", err)
	}
	return writeJSON(filepath.Join(runDir, "setup_result.json"), res)
}

func normalizeSetupResult(res *SetupResult) {
	if res == nil {
		return
	}
	res.ReadinessEvidence = truncateString(res.ReadinessEvidence, maxSetupResultTextLength)
	res.RepairGuidance = truncateString(res.RepairGuidance, maxSetupResultTextLength)
	res.Error = truncateString(res.Error, maxSetupResultTextLength)
	if len(res.Commands) > maxSetupResultCommands {
		res.Commands = res.Commands[:maxSetupResultCommands]
	}
	for i := range res.Commands {
		res.Commands[i].Run = truncateString(res.Commands[i].Run, profile.MaxSetupCommandRunLength)
		res.Commands[i].Why = truncateString(res.Commands[i].Why, profile.MaxSetupCommandWhyLength)
		res.Commands[i].StdoutExcerpt = truncateExcerpt(res.Commands[i].StdoutExcerpt)
		res.Commands[i].StderrExcerpt = truncateExcerpt(res.Commands[i].StderrExcerpt)
	}
	if len(res.SuccessfulCommands) > maxSetupResultCommands {
		res.SuccessfulCommands = res.SuccessfulCommands[:maxSetupResultCommands]
	}
	if len(res.InspectedFiles) > maxSetupResultFiles {
		res.InspectedFiles = res.InspectedFiles[:maxSetupResultFiles]
	}
	for i := range res.InspectedFiles {
		res.InspectedFiles[i] = truncateString(res.InspectedFiles[i], 512)
	}
}

// LoadSetupEnvironmentUpdate reads the persisted environment_update.json.
// Returns (nil, nil) when the file does not exist so callers can probe
// unconditionally.
func LoadSetupEnvironmentUpdate(runDir string) (*SetupEnvironmentUpdate, error) {
	path := filepath.Join(runDir, "environment_update.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var update SetupEnvironmentUpdate
	if err := json.Unmarshal(data, &update); err != nil {
		return nil, err
	}
	return &update, nil
}

// loadSetupRunEvidence reads the persisted setup_result.json and
// environment_update.json from runDir. Errors are logged to stderr and the
// helper returns nil, nil so a stale or missing file never blocks the loop.
func loadSetupRunEvidence(runDir, runID string) (*SetupResult, *SetupEnvironmentUpdate) {
	res, err := LoadSetupResult(runDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "galley: could not load setup result for run %s: %v\n", runID, err)
	}
	update, uerr := LoadSetupEnvironmentUpdate(runDir)
	if uerr != nil {
		fmt.Fprintf(os.Stderr, "galley: could not load setup environment update for run %s: %v\n", runID, uerr)
	}
	return res, update
}

// appendSetupReadinessObligations adds the setup readiness facts (and any
// learned-plan update) to the work order so the implementation executor sees
// the same readiness evidence the supervisor will review (AC8).
func appendSetupReadinessObligations(prompt string, res *SetupResult, update *SetupEnvironmentUpdate) string {
	if res == nil {
		return prompt
	}
	var b strings.Builder
	b.WriteString("\n## Setup Readiness (Runtime)\n\n")
	fmt.Fprintf(&b, "Galley setup phase status: %s (commands attempted: %d).\n", res.Status, len(res.Commands))
	if res.Source != "" {
		fmt.Fprintf(&b, "Setup source: %s.\n", res.Source)
	}
	if res.Provider != "" {
		fmt.Fprintf(&b, "Setup provider: %s.\n", res.Provider)
	}
	if res.ReadinessEvidence != "" {
		fmt.Fprintf(&b, "Readiness evidence: %s\n", res.ReadinessEvidence)
	}
	if len(res.SuccessfulCommands) > 0 {
		b.WriteString("Successful setup plan (this is the plan the setup phase persisted):\n")
		for i, cmd := range res.SuccessfulCommands {
			fmt.Fprintf(&b, "  %d. `%s`", i+1, cmd.Run)
			if cmd.Why != "" {
				fmt.Fprintf(&b, " — %s", cmd.Why)
			}
			b.WriteString("\n")
		}
	}
	if update != nil && update.Changed {
		fmt.Fprintf(&b, "Galley updated environment.yaml setup field at %s (%s).\n", update.ProfilePath, update.Reason)
	} else if res.Status == SetupStatusReady {
		b.WriteString("Setup readiness was confirmed without changing environment.yaml.\n")
	}
	return prompt + b.String()
}

// LoadSetupResult reads the persisted setup_result.json. Returns (nil, nil)
// when the file does not exist so callers can probe unconditionally.
func LoadSetupResult(runDir string) (*SetupResult, error) {
	path := filepath.Join(runDir, "setup_result.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var res SetupResult
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// WriteSetupEnvironmentUpdate persists the profile-rewrite audit record.
func WriteSetupEnvironmentUpdate(runDir string, update *SetupEnvironmentUpdate) error {
	if update == nil {
		return nil
	}
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		return fmt.Errorf("create setup run dir: %w", err)
	}
	return writeJSON(filepath.Join(runDir, "environment_update.json"), update)
}

func setupCommandTimeout(t task.Task) time.Duration {
	if t.ExecutionPolicy.TimeoutMS > 0 {
		return time.Duration(t.ExecutionPolicy.TimeoutMS) * time.Millisecond
	}
	return 30 * time.Minute
}

func truncateExcerpt(s string) string {
	s = strings.TrimSpace(s)
	return truncateString(s, maxSetupResultExcerptLength)
}

func truncateString(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return "…" + s[len(s)-max:]
}

// discoverRepositorySignals returns a small set of repository setup signal
// paths (manifests, lockfiles, setup docs) the daemon surfaces to the setup
// executor. The list is intentionally bounded so the work order payload stays
// small.
func discoverRepositorySignals(workDir string) []string {
	candidates := []string{
		"package.json", "package-lock.json", "pnpm-lock.yaml", "yarn.lock",
		"go.mod", "go.sum",
		"pyproject.toml", "poetry.lock", "requirements.txt", "Pipfile.lock",
		"Cargo.toml", "Cargo.lock",
		"Gemfile", "Gemfile.lock",
		"build.gradle", "build.gradle.kts", "pom.xml",
		"Makefile", "Taskfile.yml",
		".tool-versions", "mise.toml", ".nvmrc",
		"README.md", "CONTRIBUTING.md", "docs/setup.md",
	}
	out := make([]string, 0, 8)
	for _, name := range candidates {
		if _, err := os.Stat(filepath.Join(workDir, name)); err == nil {
			out = append(out, name)
		}
	}
	if scripts, err := os.ReadDir(filepath.Join(workDir, "scripts")); err == nil {
		for _, entry := range scripts {
			if !entry.IsDir() {
				out = append(out, filepath.Join("scripts", entry.Name()))
			}
		}
	}
	return out
}

func marshalSetupExecutorRequest(opts SetupExecutorPreflightOptions, signals []string) ([]byte, error) {
	request := map[string]any{
		"task":               opts.Task,
		"environment":        opts.Profiles.Environment,
		"quality":            opts.Profiles.Quality,
		"repository_signals": signals,
		"worktree":           opts.WorkDir,
	}
	return json.MarshalIndent(request, "", "  ")
}

func buildSetupExecutorCommandPlan(opts SetupExecutorPreflightOptions, payload []byte) (runner.Command, string, error) {
	provider := setupExecutorProvider(opts.Task)
	switch provider {
	case "codex":
		cmd, err := buildCodexSetupExecutorCommandPlan(opts, payload)
		return cmd, provider, err
	default:
		cmd, err := buildClaudeSetupExecutorCommandPlan(opts, payload)
		return cmd, "claude", err
	}
}

// setupExecutorProvider mirrors creatorProvider: the setup executor follows
// the task implementation executor backend (AC4) so the same provider runs
// setup, optional skeleton creation, and implementation.
func setupExecutorProvider(t task.Task) string {
	switch t.Executor.CLI {
	case "codex":
		return "codex"
	default:
		return "claude"
	}
}

func buildClaudeSetupExecutorCommandPlan(opts SetupExecutorPreflightOptions, payload []byte) (runner.Command, error) {
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
		PromptMode:     "replace",
		PermissionMode: "bypassPermissions",
		MaxBudgetUSD:   opts.Task.Executor.MaxBudgetUSDValue(),
		PluginDirs:     []string{guardDir},
		AttemptDir:     opts.RunDir,
	})
	if err != nil {
		return runner.Command{}, fmt.Errorf("plan setup executor: %w", err)
	}
	commandPlan.Env = runner.RestrictedEnv("GALLEY_CLAUDE_GUARD_MODE=setup_executor")
	return commandPlan, nil
}

func buildCodexSetupExecutorCommandPlan(opts SetupExecutorPreflightOptions, payload []byte) (runner.Command, error) {
	codexOpts := runner.CodexFromTask(opts.Task)
	codexOpts.Bin = opts.CodexBin
	if codexOpts.Bin == "" {
		codexOpts.Bin = "codex"
	}
	codexOpts.WorkDir = opts.WorkDir
	codexOpts.Prompt = string(payload)
	codexOpts.SystemPrompt = prompts.SetupExecutorCodex()
	codexOpts.JSONSchema = runner.CodexCompatibleOutputSchema(schemas.SetupResult)
	codexOpts.AttemptDir = opts.RunDir

	commandPlan, err := runner.CodexCommandPlan(codexOpts)
	if err != nil {
		return runner.Command{}, fmt.Errorf("plan setup executor: %w", err)
	}
	return commandPlan, nil
}

func writeSetupExecutorCommandPlan(runDir string, commandPlan runner.Command) error {
	planPath := filepath.Join(runDir, "setup_executor_command_plan.json")
	auditPlan := commandPlan
	auditPlan.Env = nil
	return writeJSON(planPath, auditPlan)
}

func runSetupExecutorCommand(ctx context.Context, opts SetupExecutorPreflightOptions, commandPlan runner.Command) (runner.RunResult, error) {
	stdoutPath := filepath.Join(opts.RunDir, "setup_executor.stdout.jsonl")
	stderrPath := filepath.Join(opts.RunDir, "setup_executor.stderr.log")
	return runner.RunCommand(ctx, commandPlan, runner.RunOptions{
		Timeout:    setupCommandTimeout(opts.Task),
		StdoutPath: stdoutPath,
		StderrPath: stderrPath,
		TailBytes:  16384,
	})
}

// resolveSetupExecutorResult parses the setup executor's structured result
// from the provider's canonical output surface. Codex emits the final JSON
// message through `--output-last-message`; Claude streams the JSON through
// stdout JSONL.
func resolveSetupExecutorResult(opts SetupExecutorPreflightOptions, stdoutTail string) (*SetupResult, error) {
	if setupExecutorProvider(opts.Task) == "codex" {
		lastMessagePath := filepath.Join(opts.RunDir, runner.CodexLastMessageFilename)
		if data, err := os.ReadFile(lastMessagePath); err == nil {
			if res, ok := parseSetupResultText(string(data)); ok {
				return res, nil
			}
		}
	}
	if res, ok := parseSetupResultText(stdoutTail); ok {
		return res, nil
	}
	// Best-effort fallback: scan the stream line by line for an embedded JSON object.
	for _, line := range strings.Split(strings.TrimSpace(stdoutTail), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var event map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &event); err == nil {
			for _, key := range []string{"result", "response", "message"} {
				raw, ok := event[key]
				if !ok {
					continue
				}
				var text string
				if err := json.Unmarshal(raw, &text); err == nil {
					if res, ok := parseSetupResultText(text); ok {
						return res, nil
					}
				}
				if res, ok := parseSetupResultRaw(raw); ok {
					return res, nil
				}
			}
		}
	}
	return nil, fmt.Errorf("setup executor result JSON not found")
}

func parseSetupResultText(text string) (*SetupResult, bool) {
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start < 0 || end < start {
		return nil, false
	}
	return parseSetupResultRaw([]byte(text[start : end+1]))
}

func parseSetupResultRaw(data []byte) (*SetupResult, bool) {
	var res SetupResult
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, false
	}
	if res.Status == "" || res.Commands == nil {
		return nil, false
	}
	normalizeSetupResult(&res)
	return &res, true
}
