package daemon

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/shinpr/galley/internal/daemonconfig"
	"github.com/shinpr/galley/internal/executorflow"
	"github.com/shinpr/galley/internal/galleyhome"
	"github.com/shinpr/galley/internal/inputfiles"
	"github.com/shinpr/galley/internal/jsonio"
	setuppreflight "github.com/shinpr/galley/internal/preflight/setup"
	skeletonpreflight "github.com/shinpr/galley/internal/preflight/skeleton"
	"github.com/shinpr/galley/internal/proc"
	"github.com/shinpr/galley/internal/profile"
	"github.com/shinpr/galley/internal/provider"
	"github.com/shinpr/galley/internal/queue"
	"github.com/shinpr/galley/internal/runartifact"
	"github.com/shinpr/galley/internal/runner"
	"github.com/shinpr/galley/internal/supervisor"
	"github.com/shinpr/galley/internal/task"
	"github.com/shinpr/galley/internal/taskstate"
	"github.com/shinpr/galley/internal/version"
	"github.com/shinpr/galley/internal/workspace"
)

// validationEvidence is the audit-friendly payload written to
// runs/<run-id>/validation.json. It is a superset of task.ValidationResult so
// existing readers that decode `errors`, `warnings`, or `task` keep working,
// while supervisor reviewers and downstream tools can rely on `valid`,
// `task_id`, `schema_version`, and `generated_at` for evidence.
type validationEvidence struct {
	Valid         bool      `json:"valid"`
	TaskID        string    `json:"task_id"`
	SchemaVersion string    `json:"schema_version"`
	GeneratedAt   string    `json:"generated_at"`
	Errors        []string  `json:"errors"`
	Warnings      []string  `json:"warnings"`
	Task          task.Task `json:"task"`
}

func newValidationEvidence(loaded task.Task, validation task.ValidationResult, now time.Time) validationEvidence {
	errs := validation.Errors
	if errs == nil {
		errs = []string{}
	}
	warnings := validation.Warnings
	if warnings == nil {
		warnings = []string{}
	}
	return validationEvidence{
		Valid:         validation.Valid(),
		TaskID:        loaded.ID,
		SchemaVersion: validationSchemaVersion(),
		GeneratedAt:   now.UTC().Format(time.RFC3339Nano),
		Errors:        errs,
		Warnings:      warnings,
		Task:          loaded,
	}
}

func validationSchemaVersion() string {
	if version.Commit != "" && version.Commit != "unknown" {
		return fmt.Sprintf("galley-%s+%s", version.Version, version.Commit)
	}
	return fmt.Sprintf("galley-%s", version.Version)
}

// Options configure the file-backed Galley daemon.
type Options struct {
	Root                   string
	SystemPromptFile       string
	JSONSchemaFile         string
	QualityProfileFile     string
	EnvironmentProfileFile string
	Once                   bool
	MaxConcurrentTasks     int
	MaxConcurrentPerRepo   int
	PollInterval           time.Duration
	ClaimTTL               time.Duration
	HeartbeatInterval      time.Duration
	IdleTimeout            time.Duration
	CommitOnAccept         bool
	OpenPR                 bool
	PollPRComments         bool
	ReplyPRComments        bool
	CleanupWorktrees       bool
	PRBase                 string
	Supervisor             string
	// SupervisorSource records where the resolved supervisor value came from
	// (cli, daemon_config, default, or environment_profile). It is set by
	// daemon CLI wiring before Preflight and then overridden per task when an
	// environment.yaml supervisor.default_cli wins for that task. The value is
	// persisted to runs/<run-id>/supervisor.json as evidence.
	SupervisorSource string
	// SupervisorModel is the repository's exact provider model override. Empty
	// preserves the supervisor CLI default.
	SupervisorModel string
	// SupervisorEffort is the profile override persisted in supervisor.json; empty keeps the CLI default.
	SupervisorEffort     string
	ShutdownTimeout      time.Duration
	DisableClaudeGuard   bool
	ClaudeGuardPluginDir string
	ClaudeBin            string
	CodexBin             string
	GrokBin              string
	GitBin               string
	GHBin                string
	// Redirected Claude providers receive credentials only in child environments.
	GLMAuthToken string
	KimiAPIKey   string
	// Notifications is the opt-in, best-effort notification command hook
	// resolved from daemon.yaml. A nil pointer disables notifications. It has
	// no CLI flag because the hook is operator configuration, not a
	// runtime-tunable knob.
	Notifications *daemonconfig.NotificationConfig
	// notifyTimeout overrides the notification hook timeout. It is unexported
	// because it is a test seam, not operator configuration: zero resolves to
	// notify.DefaultTimeout, which is what production always uses. Tests set a
	// short value to prove a stuck command is killed by the timeout without
	// waiting the full default bound.
	notifyTimeout    time.Duration
	notifyDispatcher *notificationDispatcher
	dependencies     *daemonDependencies
	Explicit         ExplicitOptions
}

type daemonDependencies struct {
	stageExecutorOutput  func(context.Context, Options, string, string, []string) error
	captureDiffArtifacts func(context.Context, string, string, string, workspace.Options) (executorflow.DiffArtifacts, error)
	supervisorRunner     func(context.Context, Options, supervisor.Evidence, string, string) (supervisor.Verdict, error)
	setupExecutorRunner  func(context.Context, setuppreflight.Options) (*setuppreflight.Result, error)
}

func defaultDaemonDependencies() daemonDependencies {
	return daemonDependencies{
		stageExecutorOutput:  defaultStageExecutorOutput,
		captureDiffArtifacts: executorflow.CaptureDiffArtifacts,
		supervisorRunner:     defaultSupervisorRunner,
		setupExecutorRunner:  setuppreflight.RunExecutor,
	}
}

func (opts Options) daemonDependencies() daemonDependencies {
	defaults := defaultDaemonDependencies()
	if opts.dependencies == nil {
		return defaults
	}
	deps := *opts.dependencies
	if deps.stageExecutorOutput == nil {
		deps.stageExecutorOutput = defaults.stageExecutorOutput
	}
	if deps.captureDiffArtifacts == nil {
		deps.captureDiffArtifacts = defaults.captureDiffArtifacts
	}
	if deps.supervisorRunner == nil {
		deps.supervisorRunner = defaults.supervisorRunner
	}
	if deps.setupExecutorRunner == nil {
		deps.setupExecutorRunner = defaults.setupExecutorRunner
	}
	return deps
}

type ExplicitOptions struct {
	Root                   bool
	SystemPromptFile       bool
	JSONSchemaFile         bool
	QualityProfileFile     bool
	EnvironmentProfileFile bool
	MaxConcurrentTasks     bool
	MaxConcurrentPerRepo   bool
	PollInterval           bool
	ClaimTTL               bool
	ShutdownTimeout        bool
	IdleTimeout            bool
	CommitOnAccept         bool
	OpenPR                 bool
	PollPRComments         bool
	ReplyPRComments        bool
	CleanupWorktrees       bool
	PRBase                 bool
	Supervisor             bool
}

// DefaultSupervisor is the built-in supervisor adapter used when daemon
// startup does not receive an explicit --supervisor value.
const DefaultSupervisor = "claude"

// Supervisor source labels. Persisted in run evidence so reviewers can
// tell whether the per-task supervisor came from a repository environment
// profile, a daemon CLI startup flag, daemon.yaml, or the built-in default.
const (
	SupervisorSourceRepoProfile  = "environment_profile"
	SupervisorSourceCLI          = "cli"
	SupervisorSourceDaemonConfig = "daemon_config"
	SupervisorSourceDefault      = "default"
)

const (
	SupervisorModelSourceRepoProfile = "environment_profile"
	SupervisorModelSourceCLIDefault  = "cli_default"
)

const (
	SupervisorEffortSourceRepoProfile = "environment_profile"
	SupervisorEffortSourceCLIDefault  = "cli_default"
)

// Run starts the daemon loop.
func Run(ctx context.Context, opts Options) error {
	var err error
	opts, err = Preflight(opts)
	if err != nil {
		return err
	}
	// Install the active child registry so executor and supervisor subprocess
	// process groups are tracked for the lifetime of this daemon process.
	// galley daemon stop --force reads the same file from disk and SIGKILLs
	// any pgids that are still alive after the daemon exits, so a daemon
	// teardown does not orphan executor/supervisor children that were
	// intentionally started in their own process groups.
	registry := proc.NewChildRegistry(proc.ChildRegistryPath(opts.Root))
	proc.SetDefaultChildRegistry(registry)
	defer proc.SetDefaultChildRegistry(nil)
	defer func() { _ = registry.Clear() }()
	opts.notifyDispatcher = newNotificationDispatcher(ctx)
	defer opts.notifyDispatcher.Wait()
	if err := recoverInterruptedRunningTasks(opts.Root); err != nil {
		return err
	}
	if opts.Once {
		return runExecutionDrain(ctx, opts)
	}
	return runNormalDaemon(ctx, opts)
}

// runExecutionDrain implements `galley daemon run --once`. It drains eligible
// queued work through the shared execution runner and exits once a pass claims
// nothing, instead of becoming a long-running daemon. Normal-daemon scheduling
// routes through the same execution runner so run-once and normal mode share
// claim/execute behavior.
func runExecutionDrain(ctx context.Context, opts Options) error {
	var firstErr error
	for {
		processed, err := processQueuedTasks(ctx, opts)
		if err != nil && firstErr == nil {
			firstErr = err
		}
		if processed == 0 {
			return firstErr
		}
	}
}

// runNormalDaemon keeps maintenance responsive while task execution may block
// for a long executor attempt.
func runNormalDaemon(ctx context.Context, opts Options) error {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		runMaintenanceRunner(ctx, opts)
	}()
	go func() {
		defer wg.Done()
		runExecutionRunner(ctx, opts)
	}()
	wg.Wait()
	// A cancelled context is the normal stop signal (SIGTERM/SIGINT); reporting
	// context.Canceled as an error makes `galley daemon run` exit non-zero on a
	// clean shutdown and read as a failed unit under systemd.
	if err := ctx.Err(); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

// runMaintenanceRunner owns PR comment polling and PR worktree cleanup.
func runMaintenanceRunner(ctx context.Context, opts Options) {
	ticker := time.NewTicker(opts.PollInterval)
	defer ticker.Stop()
	for {
		if err := runMaintenanceCycle(ctx, opts); err != nil {
			fmt.Fprintf(os.Stderr, "galley: maintenance failed: %v\n", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// runExecutionRunner preserves the existing serialized execution pass: each
// pass waits for claimed tasks to finish before the next execution tick.
func runExecutionRunner(ctx context.Context, opts Options) {
	ticker := time.NewTicker(opts.PollInterval)
	defer ticker.Stop()
	for {
		if _, err := processQueuedTasks(ctx, opts); err != nil {
			fmt.Fprintf(os.Stderr, "galley: iteration failed: %v\n", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// runMaintenanceCycle keeps polling and cleanup serialized inside maintenance.
func runMaintenanceCycle(ctx context.Context, opts Options) error {
	var errs []error
	if err := pollPRComments(ctx, opts); err != nil {
		errs = append(errs, fmt.Errorf("poll PR comments: %w", err))
	}
	if err := cleanupWorktrees(ctx, opts); err != nil {
		errs = append(errs, fmt.Errorf("cleanup worktrees: %w", err))
	}
	return errors.Join(errs...)
}

func processQueuedTasks(ctx context.Context, opts Options) (int, error) {
	processed, err := processAvailable(ctx, opts)
	if err != nil {
		return processed, fmt.Errorf("process available tasks: %w", err)
	}
	return processed, nil
}

// Preflight resolves daemon options and verifies startup prerequisites.
func Preflight(opts Options) (Options, error) {
	opts = opts.withDefaults()
	if !provider.IsSupervisor(opts.Supervisor) {
		return Options{}, fmt.Errorf("supervisor must be one of: %s", strings.Join(provider.SupervisorIDs(), ", "))
	}
	if err := validateProviderCredential(opts.Supervisor, opts); err != nil {
		return Options{}, fmt.Errorf("supervisor is %q: %w", opts.Supervisor, err)
	}
	if err := queue.EnsureLayout(opts.Root); err != nil {
		return Options{}, err
	}
	return opts, nil
}

func claudeProviderOptions(providerID string, opts Options) runner.ClaudeProviderOptions {
	return runner.ClaudeProviderOptions{
		Provider: providerID,
		Credentials: runner.ClaudeCredentials{
			GLMAuthToken: opts.GLMAuthToken,
			KimiAPIKey:   opts.KimiAPIKey,
		},
	}
}

func validateProviderCredential(providerID string, opts Options) error {
	transport, ok := provider.TransportFor(providerID)
	if !ok || transport != provider.TransportClaude {
		return nil
	}
	return runner.ValidateClaudeProvider(claudeProviderOptions(providerID, opts))
}

func (opts Options) withDefaults() Options {
	if opts.Root == "" {
		opts.Root = galleyhome.DefaultRoot()
	}
	if opts.Supervisor == "" {
		opts.Supervisor = DefaultSupervisor
		if opts.SupervisorSource == "" {
			opts.SupervisorSource = SupervisorSourceDefault
		}
	}
	if opts.SupervisorSource == "" {
		opts.SupervisorSource = SupervisorSourceDefault
	}
	if opts.MaxConcurrentTasks <= 0 {
		opts.MaxConcurrentTasks = 1
	}
	if !opts.Explicit.MaxConcurrentPerRepo && opts.MaxConcurrentPerRepo == 0 {
		opts.MaxConcurrentPerRepo = 1
	}
	if opts.MaxConcurrentPerRepo < 0 {
		opts.MaxConcurrentPerRepo = 0
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = 10 * time.Second
	}
	if opts.ClaimTTL <= 0 {
		opts.ClaimTTL = 30 * time.Minute
	}
	if opts.ShutdownTimeout <= 0 {
		opts.ShutdownTimeout = 5 * time.Minute
	}
	if opts.IdleTimeout <= 0 {
		opts.IdleTimeout = 10 * time.Minute
	}
	// Heartbeat cadence is derived only from claim_ttl: min(claim_ttl/4, 1m).
	opts.HeartbeatInterval = opts.ClaimTTL / 4
	if opts.HeartbeatInterval <= 0 {
		opts.HeartbeatInterval = time.Minute
	}
	if opts.HeartbeatInterval > time.Minute {
		opts.HeartbeatInterval = time.Minute
	}
	if opts.OpenPR && !opts.Explicit.CommitOnAccept {
		opts.CommitOnAccept = true
	}
	return opts
}

func processAvailable(ctx context.Context, opts Options) (int, error) {
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	default:
	}
	if err := queue.EnsureLayout(opts.Root); err != nil {
		return 0, err
	}
	if err := queue.RecoverStaleClaims(opts.Root, opts.ClaimTTL, time.Now()); err != nil {
		return 0, err
	}
	queued, err := queue.QueuedTasks(opts.Root)
	if err != nil {
		return 0, err
	}
	if len(queued) == 0 {
		return 0, nil
	}

	limit := opts.MaxConcurrentTasks
	if limit > len(queued) {
		limit = len(queued)
	}

	var wg sync.WaitGroup
	errs := make(chan error, limit)
	execCtx, stopExecCtx := gracefulTaskContext(ctx, opts.ShutdownTimeout)
	defer stopExecCtx()
	repoCounts, err := queue.RunningRepoCounts(opts.Root)
	if err != nil {
		return 0, err
	}
	claimedCount, firstClaimErr := claimAvailableTasks(ctx, execCtx, opts, queued, limit, repoCounts, &wg, errs)
	// This wait is load-bearing: each daemon iteration completes its claimed
	// task goroutines before the next stale-recovery pass can run. That prevents
	// Galley from requeueing work it started in the same process.
	wg.Wait()
	close(errs)

	firstErr := firstClaimErr
	for err := range errs {
		if firstErr == nil {
			firstErr = err
		}
	}
	return claimedCount, firstErr
}

func claimAvailableTasks(daemonCtx, execCtx context.Context, opts Options, queued []string, limit int, repoCounts map[string]int, wg *sync.WaitGroup, errs chan<- error) (int, error) {
	claimedCount := 0
	var firstClaimErr error
	for _, queuedPath := range queued {
		if claimedCount >= limit {
			break
		}
		select {
		case <-daemonCtx.Done():
			return claimedCount, firstNonNil(firstClaimErr, daemonCtx.Err())
		default:
		}
		repoKey, skip, err := repoKeyForClaim(opts, queuedPath, repoCounts)
		if err != nil {
			if firstClaimErr == nil {
				firstClaimErr = err
			}
			continue
		}
		if skip {
			continue
		}
		claimed, err := queue.ClaimTask(opts.Root, queuedPath)
		if err != nil {
			if errors.Is(err, queue.ErrClaimConflict) {
				continue
			}
			if firstClaimErr == nil {
				firstClaimErr = err
			}
			continue
		}
		claimedCount++
		if err := queue.WriteOwner(claimed, currentRunningOwner()); err != nil {
			// Owner metadata is a best-effort recovery aid; if it cannot be written
			// the task still proceeds and falls back to TTL-based recovery.
			fmt.Fprintf(os.Stderr, "galley: record task owner failed for %s: %v\n", claimed, err)
		}
		if repoKey != "" {
			repoCounts[repoKey]++
		}
		wg.Add(1)
		go func(path string) {
			defer wg.Done()
			defer func() { _ = queue.RemoveOwner(path) }()
			if err := processClaimedTask(execCtx, daemonCtx, opts, path); err != nil {
				errs <- err
			}
		}(claimed)
	}
	return claimedCount, firstClaimErr
}

func repoKeyForClaim(opts Options, queuedPath string, repoCounts map[string]int) (string, bool, error) {
	conflict, err := queuedHasClaimConflict(opts.Root, queuedPath)
	if err != nil {
		return "", false, err
	}
	if conflict {
		return "", true, nil
	}
	if opts.MaxConcurrentPerRepo <= 0 {
		return "", false, nil
	}
	loaded, err := task.Load(queuedPath)
	if err != nil {
		return "", false, fmt.Errorf("load queued task for repo limit %s: %w", queuedPath, err)
	}
	if loaded.Scope.CWD == "" {
		return "", false, nil
	}
	repoKey := queue.RepoConcurrencyKey(loaded.Scope.CWD)
	if repoCounts[repoKey] >= opts.MaxConcurrentPerRepo {
		return repoKey, true, nil
	}
	return repoKey, false, nil
}

func firstNonNil(primary, fallback error) error {
	if primary != nil {
		return primary
	}
	return fallback
}

func queuedHasClaimConflict(root, queuedPath string) (bool, error) {
	if exists, err := pathExistsForClaim(queuedPath + ".lock"); err != nil || exists {
		return exists, err
	}
	runningState, err := task.WorkflowStateForTransition(task.StatusQueued, task.StatusRunning)
	if err != nil {
		return false, err
	}
	runningPath := task.TaskStatePath(root, runningState, filepath.Base(queuedPath))
	if exists, err := pathExistsForClaim(runningPath); err != nil || exists {
		return exists, err
	}
	if exists, err := pathExistsForClaim(runningPath + ".lock"); err != nil || exists {
		return exists, err
	}
	return false, nil
}

func pathExistsForClaim(path string) (bool, error) {
	if _, err := os.Stat(path); err == nil {
		return true, nil
	} else if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	} else {
		return false, fmt.Errorf("inspect claim conflict path %s: %w", path, err)
	}
}

func gracefulTaskContext(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.WithoutCancel(parent))
	done := make(chan struct{})
	var once sync.Once
	stop := context.AfterFunc(parent, func() {
		if timeout <= 0 {
			cancel()
			return
		}
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		select {
		case <-timer.C:
			cancel()
		case <-done:
		}
	})
	return ctx, func() {
		once.Do(func() { close(done) })
		stop()
		cancel()
	}
}

func processClaimedTask(execCtx, daemonCtx context.Context, opts Options, runningPath string) error {
	var runDir string
	// Notification observes published terminal state and cannot change it.
	defer func() { notifyTerminalPublication(execCtx, opts, runningPath, &runDir) }()

	loaded, err := loadClaimedTask(runningPath)
	if err != nil {
		return taskstate.RecoverUnreadableClaimToFailed(opts.Root, runningPath, err)
	}
	stopHeartbeat := startHeartbeat(execCtx, runningPath, opts.HeartbeatInterval)
	defer stopHeartbeat()

	validation, err := validateClaimedTask(&loaded)
	if err != nil {
		return taskstate.FailMoveToStatus(opts.Root, runningPath, &loaded, err)
	}
	runID, runDir, err := initializeRunEvidence(opts.Root, runningPath, loaded, validation)
	if err != nil {
		return failClaimedStage(opts.Root, runningPath, &loaded, "run_evidence", "run_evidence_failed", err, "")
	}

	// Profile resolution must happen before workspace.Prepare so that the
	// resolved environment profile (specifically pr.base) can supply a
	// start-point ref to the brand-new task branch instead of inheriting
	// the source repository's current HEAD. The resolved bundle is threaded
	// into runSupervisorLoop so the supervisor loop never re-loads it.
	profiles, resolvedProfiles, err := loadAndPersistTaskProfiles(opts, &loaded, runDir)
	if err != nil {
		return failClaimedStage(opts.Root, runningPath, &loaded, "run_evidence", "run_evidence_failed", err, runDir)
	}
	effectiveOpts := resolveEffectiveTaskOptions(opts, profiles).apply(opts)

	// Keep authored fields unchanged while all provider roles use this run's resolution.
	var envExecutor *profile.ExecutorDefault
	if profiles.Environment != nil {
		envExecutor = profiles.Environment.Executor
	}
	effectiveExecutor := task.ResolveEffectiveExecutor(loaded.Executor, envExecutor)
	if err := task.ValidateEffectiveExecutor(effectiveExecutor); err != nil {
		return failClaimedStage(opts.Root, runningPath, &loaded, "executor_preflight", "executor_config_failed", err, runDir)
	}

	// Per-task provider overrides bypass startup Preflight, so validate before setup and execution.
	if credentialErr := validateProviderCredential(effectiveOpts.Supervisor, opts); credentialErr != nil {
		return failClaimedStage(opts.Root, runningPath, &loaded, "supervisor_preflight", "supervisor_config_failed", fmt.Errorf("supervisor is %q: %w", effectiveOpts.Supervisor, credentialErr), runDir)
	}
	if credentialErr := validateProviderCredential(effectiveExecutor.CLI, opts); credentialErr != nil {
		return failClaimedStage(opts.Root, runningPath, &loaded, "executor_preflight", "executor_config_failed", fmt.Errorf("executor is %q: %w", effectiveExecutor.CLI, credentialErr), runDir)
	}

	prepared, err := prepareClaimedWorkspace(execCtx, opts, profiles, runningPath, runDir, &loaded, effectiveExecutor)
	if err != nil {
		return taskstate.FailMoveToStatus(opts.Root, runningPath, &loaded, err)
	}
	// Setup runs after workspace preparation and before skeleton or implementation roles.
	var setupRes *setuppreflight.Result
	var setupUpdate *setuppreflight.EnvironmentUpdate
	setupReused := false
	if prepared.WorktreeReused {
		setupRes, setupReused, err = reuseReadySetup(opts.Root, loaded.ID, runDir, effectiveExecutor)
		if err != nil {
			return failClaimedStage(opts.Root, runningPath, &loaded, setuppreflight.Phase, setuppreflight.FailedKind, preflightReuseError(setuppreflight.Phase, err), runDir)
		}
	}
	var setupErr error
	if !setupReused {
		setupRes, setupUpdate, setupErr = setuppreflight.Run(execCtx, setuppreflight.Options{
			Task:                   task.WithExecutor(loaded, effectiveExecutor),
			WorkDir:                prepared.CWD,
			RunDir:                 runDir,
			Profiles:               profiles,
			ClaudeBin:              opts.ClaudeBin,
			CodexBin:               opts.CodexBin,
			GrokBin:                opts.GrokBin,
			GLMAuthToken:           opts.GLMAuthToken,
			KimiAPIKey:             opts.KimiAPIKey,
			EnvironmentProfilePath: resolvedProfiles.EnvironmentProfileFile,
			ExecutorRunner:         opts.daemonDependencies().setupExecutorRunner,
		})
	}
	if setupErr != nil {
		return failClaimedStage(opts.Root, runningPath, &loaded, setuppreflight.Phase, setuppreflight.FailedKind, setupErr, runDir)
	}
	// Apply setup readiness evidence (and any persisted profile change) to the
	// running task before the implementation work order is built so the
	// supervisor and executor share the same readiness facts.
	if !setupReused {
		applySetupResultToTask(&loaded, setupRes, setupUpdate)
	}
	if setupRes != nil && !setupReused {
		if err := task.Save(runningPath, loaded); err != nil {
			return failClaimedStage(opts.Root, runningPath, &loaded, setuppreflight.Phase, setuppreflight.FailedKind, err, runDir)
		}
	}
	// Optional acceptance skeleton preflight runs after inputfiles.Prepare and
	// before the first executor attempt. The stage is a no-op when the task
	// omits preflight.acceptance_skeleton.enabled or sets it to false. When the stage fails the daemon does not run the executor and
	// surfaces the failure through task status and run evidence.
	if cfg := loaded.Preflight; cfg != nil && cfg.AcceptanceSkeleton.IsEnabled() {
		var res *skeletonpreflight.Result
		preflightReused := false
		if prepared.WorktreeReused {
			res, preflightReused, err = reuseCompletedAcceptanceSkeleton(opts.Root, loaded.ID, runDir, effectiveExecutor)
			if err != nil {
				return failClaimedStage(opts.Root, runningPath, &loaded, "acceptance_skeleton_preflight", "acceptance_skeleton_preflight_failed", preflightReuseError("acceptance skeleton", err), runDir)
			}
		}
		var perr error
		if !preflightReused {
			res, perr = skeletonpreflight.Run(execCtx, skeletonpreflight.Options{
				Task:         task.WithExecutor(loaded, effectiveExecutor),
				WorkDir:      prepared.CWD,
				RunDir:       runDir,
				Profiles:     profiles,
				GitBin:       opts.GitBin,
				ClaudeBin:    opts.ClaudeBin,
				CodexBin:     opts.CodexBin,
				GrokBin:      opts.GrokBin,
				GLMAuthToken: opts.GLMAuthToken,
				KimiAPIKey:   opts.KimiAPIKey,
			})
		}
		if perr != nil {
			return failClaimedStage(opts.Root, runningPath, &loaded, "acceptance_skeleton_preflight", "acceptance_skeleton_preflight_failed", perr, runDir)
		}
		if res != nil && !preflightReused {
			skeletonpreflight.ApplyToTask(&loaded, res)
			if err := task.Save(runningPath, loaded); err != nil {
				return failClaimedStage(opts.Root, runningPath, &loaded, "acceptance_skeleton_preflight", "acceptance_skeleton_preflight_failed", err, runDir)
			}
			if err := task.Save(runartifact.Path(runDir, runartifact.EffectiveTaskSnapshotFilename), executionTask(loaded, prepared.CWD, effectiveExecutor)); err != nil {
				return failClaimedStage(opts.Root, runningPath, &loaded, "run_evidence", "run_evidence_failed", err, runDir)
			}
		}
	}
	return runSupervisorLoop(execCtx, daemonCtx, effectiveOpts, runningPath, &loaded, prepared, profiles, runDir, runID, effectiveExecutor)
}

func failClaimedStage(root, runningPath string, loaded *task.Task, phase, kind string, err error, runDir string) error {
	appendFailureAttempt(loaded, phase, kind, err, runDir)
	return taskstate.FailMoveToStatus(root, runningPath, loaded, err)
}

// loadAndPersistTaskProfiles resolves the quality and environment profiles for
// the claimed task and writes the run evidence file (profiles.json) into the
// run directory. The payload keeps the resolved file paths and profile bundle
// together so evidence readers do not need to re-run profile resolution.
func loadAndPersistTaskProfiles(opts Options, loaded *task.Task, runDir string) (profile.Bundle, resolvedProfileFiles, error) {
	resolved, profiles, err := loadTaskProfiles(opts, loaded.Scope.CWD)
	if err != nil {
		return profile.Bundle{}, resolvedProfileFiles{}, err
	}
	if err := writeJSON(runartifact.Path(runDir, runartifact.ProfilesFilename), struct {
		Resolved resolvedProfileFiles `json:"resolved"`
		Bundle   profile.Bundle       `json:"bundle"`
	}{Resolved: resolved, Bundle: profiles}); err != nil {
		return profile.Bundle{}, resolvedProfileFiles{}, err
	}
	return profiles, resolved, nil
}

// applySetupResultToTask records setup readiness evidence on the running task
// so the implementation work order and supervisor evidence carry the same
// facts. The setup outcome is also appended to task.verification.commands so
// the task verification history and rendered PR/task output always include the
// setup readiness fact. When a learned plan was persisted to environment.yaml,
// the change is additionally surfaced as a Risk-style note so PR/task output
// reflects the profile update.
func applySetupResultToTask(loaded *task.Task, res *setuppreflight.Result, update *setuppreflight.EnvironmentUpdate) {
	if loaded == nil || res == nil {
		return
	}
	note := fmt.Sprintf("setup status=%s commands=%d", res.Status, len(res.Commands))
	if res.ReadinessEvidence != "" {
		note = note + " - " + res.ReadinessEvidence
	}
	// Persist setup evidence in task.verification.commands so it shows up
	// in the task verification history and the rendered PR/task output. The
	// command label is a stable pseudo-command operators can recognize even
	// without inspecting the run directory, and the excerpt names the setup
	// source so readers can tell authored vs learned without opening
	// setup_result.json.
	setupCmd := "<galley:setup>"
	if res.Provider != "" {
		setupCmd = fmt.Sprintf("<galley:setup:%s>", res.Provider)
	}
	excerpt := note + fmt.Sprintf(" source=%s", res.Source)
	if update != nil && update.Changed {
		excerpt = excerpt + fmt.Sprintf(" environment.yaml=%s (%s)", update.ProfilePath, update.Reason)
	} else if res.Status == setuppreflight.StatusReady {
		excerpt = excerpt + " environment.yaml=unchanged"
	}
	loaded.Verification.Commands = append(loaded.Verification.Commands, task.VerificationCommand{
		Cmd:           setupCmd,
		Status:        setupVerificationStatus(res.Status),
		OutputExcerpt: excerpt,
	})
	if update != nil && update.Changed {
		// Surface profile changes as a Risk-style entry so task/PR output
		// records that environment.yaml setup was rewritten.
		appendRisk(loaded, "setup-profile-updated", "technical_debt", fmt.Sprintf("Setup executor persisted a learned plan to %s (%s). %s", update.ProfilePath, update.Reason, note), "", false)
	}
}

// setupVerificationStatus maps SetupResult.Status to the canonical
// VerificationCommand status vocabulary used by task verification history.
func setupVerificationStatus(s string) string {
	switch s {
	case setuppreflight.StatusReady:
		return "passed"
	case setuppreflight.StatusFailed:
		return "failed"
	default:
		return "skipped"
	}
}

func loadClaimedTask(runningPath string) (task.Task, error) {
	loaded, err := task.Load(runningPath)
	if err != nil {
		return task.Task{}, err
	}
	task.ResolveFileSources(runningPath, &loaded)
	task.ApplyDefaults(&loaded)
	loaded.Status = "running"
	if err := task.Save(runningPath, loaded); err != nil {
		return task.Task{}, err
	}
	return loaded, nil
}

func validateClaimedTask(loaded *task.Task) (task.ValidationResult, error) {
	validation := task.Validate(*loaded)
	if validation.Valid() {
		return validation, nil
	}
	err := fmt.Errorf("task validation failed: %v", validation.Errors)
	appendFailureAttempt(loaded, "validation", "validation_failed", err, "")
	return validation, err
}

func initializeRunEvidence(root, runningPath string, loaded task.Task, validation task.ValidationResult) (string, string, error) {
	runID := fmt.Sprintf("%s-%d", loaded.ID, time.Now().UnixNano())
	runDir := filepath.Join(root, "runs", runID)
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		return "", "", fmt.Errorf("create run dir %s: %w", runDir, err)
	}
	if err := copyFile(runningPath, runartifact.Path(runDir, runartifact.TaskSnapshotFilename)); err != nil {
		return "", "", err
	}
	evidence := newValidationEvidence(loaded, validation, time.Now())
	if err := writeJSON(runartifact.Path(runDir, runartifact.ValidationFilename), evidence); err != nil {
		return "", "", err
	}
	return runID, runDir, nil
}

type claimedWorkspace struct {
	workspace.Prepared
	ReviewContractContext supervisor.ReviewContractContext
}

func prepareClaimedWorkspace(ctx context.Context, opts Options, profiles profile.Bundle, runningPath, runDir string, loaded *task.Task, effectiveExecutor task.Executor) (claimedWorkspace, error) {
	wsOpts := workspaceOptions(opts)
	// Resolve the environment profile pr.base into a concrete git ref name and
	// pass it through workspace.Options.StartPoint so the new task branch is
	// based on origin/<pr.base> (or the local refs/heads/<pr.base> fallback)
	// instead of the source repository's current HEAD. When pr.base is empty
	// the resolved ref is "" and workspace.Prepare preserves today's behavior.
	prBase := ""
	if profiles.Environment != nil {
		prBase = profiles.Environment.PR.Base
	}
	startPoint, err := resolveWorktreeStartPoint(ctx, opts, loaded.Scope.CWD, prBase)
	if err != nil {
		appendFailureAttempt(loaded, "workspace", "workspace_failed", err, runDir)
		return claimedWorkspace{}, err
	}
	wsOpts.StartPoint = startPoint
	prepared, err := workspace.Prepare(ctx, loaded.Scope.CWD, loaded.Worktree, wsOpts)
	if err != nil {
		appendFailureAttempt(loaded, "workspace", "workspace_failed", err, runDir)
		return claimedWorkspace{}, err
	}
	if err := writeJSON(runartifact.Path(runDir, runartifact.WorkspaceFilename), prepared); err != nil {
		appendFailureAttempt(loaded, "run_evidence", "run_evidence_failed", err, runDir)
		return claimedWorkspace{}, err
	}
	if prepared.WorktreeReused {
		if err := reconcileReusedInputFiles(prepared.CWD, loaded.Files); err != nil {
			appendFailureAttempt(loaded, "input_files", "input_files_failed", err, runDir)
			return claimedWorkspace{}, err
		}
	}
	preparedFiles, err := inputfiles.Prepare(prepared.CWD, loaded.Files)
	if err != nil {
		appendFailureAttempt(loaded, "input_files", "input_files_failed", err, runDir)
		return claimedWorkspace{}, err
	}
	cleanupPrepared := true
	defer func() {
		if cleanupPrepared {
			_ = inputfiles.CleanupPrepared(preparedFiles)
		}
	}()
	if err := writeJSON(runartifact.Path(runDir, runartifact.InputFilesFilename), preparedFiles); err != nil {
		appendFailureAttempt(loaded, "run_evidence", "run_evidence_failed", err, runDir)
		return claimedWorkspace{}, err
	}
	if prepared.WorktreeReused && prepared.Dirty {
		appendRisk(loaded, "workspace-dirty", "technical_debt", "Reused worktree had uncommitted changes before executor run.", "Preserved existing worktree state and recorded git status porcelain in workspace.json.", true)
		if err := task.Save(runningPath, *loaded); err != nil {
			return claimedWorkspace{}, err
		}
	}
	if err := task.Save(runartifact.Path(runDir, runartifact.EffectiveTaskSnapshotFilename), executionTask(*loaded, prepared.CWD, effectiveExecutor)); err != nil {
		return claimedWorkspace{}, err
	}
	cleanupPrepared = false
	return claimedWorkspace{
		Prepared: prepared,
		ReviewContractContext: supervisor.ReviewContractContext{
			SourceCWD:        loaded.Scope.CWD,
			InputFilesDigest: inputfiles.ContractDigest(preparedFiles),
		},
	}, nil
}

func startHeartbeat(ctx context.Context, path string, interval time.Duration) func() {
	heartbeatCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	touch := func() {
		now := time.Now()
		if err := os.Chtimes(path, now, now); err != nil && !errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(os.Stderr, "galley: heartbeat update failed for %s: %v\n", path, err)
		}
	}
	touch()
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatCtx.Done():
				return
			case <-ticker.C:
				touch()
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

func executorStatus(result proc.RunResult, err error) string {
	if err == nil {
		return "completed"
	}
	if result.IdleTimedOut {
		return "idle_timed_out"
	}
	if result.TimedOut {
		return "timed_out"
	}
	return "failed"
}

func verificationStatus(err error) string {
	if err == nil {
		return "passed"
	}
	return "failed"
}

func writeJSON(path string, value any) error {
	return jsonio.Write(path, value)
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("read %s: %w", src, err)
	}
	if err := os.WriteFile(dst, data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", dst, err)
	}
	return nil
}
