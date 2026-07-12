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
	"github.com/shinpr/galley/internal/galleyhome"
	"github.com/shinpr/galley/internal/inputfiles"
	"github.com/shinpr/galley/internal/jsonio"
	setuppreflight "github.com/shinpr/galley/internal/preflight/setup"
	skeletonpreflight "github.com/shinpr/galley/internal/preflight/skeleton"
	"github.com/shinpr/galley/internal/profile"
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
	GitBin               string
	GHBin                string
	// GLMAuthToken is the Z.ai API token used when a task selects executor.cli
	// "glm". It is resolved from daemon.yaml (glm_api_key) and injected only
	// into the executor child environment as ANTHROPIC_AUTH_TOKEN; it never
	// reaches argv or run evidence. Empty means GLM is not configured, which is
	// only an error when a task actually requests executor.cli "glm".
	GLMAuthToken string
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
	stageExecutorOutput func(context.Context, Options, string, string, []string) error
	supervisorRunner    func(context.Context, Options, supervisor.Evidence, string, string) (supervisor.Verdict, error)
	setupExecutorRunner func(context.Context, setuppreflight.Options) (*setuppreflight.Result, error)
}

func defaultDaemonDependencies() daemonDependencies {
	return daemonDependencies{
		stageExecutorOutput: defaultStageExecutorOutput,
		supervisorRunner:    defaultSupervisorRunner,
		setupExecutorRunner: setuppreflight.RunExecutor,
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
	HeartbeatInterval      bool
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
	registry := runner.NewChildRegistry(runner.ChildRegistryPath(opts.Root))
	runner.SetDefaultChildRegistry(registry)
	defer runner.SetDefaultChildRegistry(nil)
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
	if !daemonconfig.IsValidSupervisor(opts.Supervisor) {
		return Options{}, fmt.Errorf("supervisor must be one of: %s", strings.Join(daemonconfig.SupervisorCLIs(), ", "))
	}
	// glm rides the Claude binary, so a glm supervisor needs the same token as a
	// glm executor. Fail fast at startup rather than at first review.
	if opts.Supervisor == "glm" {
		if _, err := runner.ResolveGLMToken(opts.GLMAuthToken); err != nil {
			return Options{}, fmt.Errorf("supervisor is \"glm\": %w", err)
		}
	}
	if err := queue.EnsureLayout(opts.Root); err != nil {
		return Options{}, err
	}
	return opts, nil
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
	if opts.HeartbeatInterval <= 0 {
		opts.HeartbeatInterval = opts.ClaimTTL / 4
		if opts.HeartbeatInterval <= 0 {
			opts.HeartbeatInterval = time.Minute
		}
		if opts.HeartbeatInterval > time.Minute {
			opts.HeartbeatInterval = time.Minute
		}
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
	taskCtx, stopTaskCtx := gracefulTaskContext(ctx, opts.ShutdownTimeout)
	defer stopTaskCtx()
	repoCounts, err := queue.RunningRepoCounts(opts.Root)
	if err != nil {
		return 0, err
	}
	claimedCount, firstClaimErr := claimAvailableTasks(ctx, taskCtx, opts, queued, limit, repoCounts, &wg, errs)
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

func claimAvailableTasks(ctx, taskCtx context.Context, opts Options, queued []string, limit int, repoCounts map[string]int, wg *sync.WaitGroup, errs chan<- error) (int, error) {
	claimedCount := 0
	var firstClaimErr error
	for _, queuedPath := range queued {
		if claimedCount >= limit {
			break
		}
		select {
		case <-ctx.Done():
			return claimedCount, firstNonNil(firstClaimErr, ctx.Err())
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
			if err := processClaimedTask(taskCtx, ctx, opts, path); err != nil {
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

func processClaimedTask(ctx, shutdownCtx context.Context, opts Options, runningPath string) error {
	var runDir string
	// Notification observes published terminal state and cannot change it.
	defer func() { notifyTerminalPublication(ctx, opts, runningPath, &runDir) }()

	loaded, err := loadClaimedTask(runningPath)
	if err != nil {
		return taskstate.RecoverUnreadableClaimToFailed(opts.Root, runningPath, err)
	}
	stopHeartbeat := startHeartbeat(ctx, runningPath, opts.HeartbeatInterval)
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

	// A per-task environment.yaml supervisor.default_cli: glm override bypasses
	// startup Preflight, so validate the token here — before setup/executor —
	// rather than failing at the supervisor call after a full attempt ran.
	if effectiveOpts.Supervisor == "glm" {
		if _, tokenErr := runner.ResolveGLMToken(opts.GLMAuthToken); tokenErr != nil {
			return failClaimedStage(opts.Root, runningPath, &loaded, "supervisor_preflight", "supervisor_config_failed", fmt.Errorf("supervisor is \"glm\": %w", tokenErr), runDir)
		}
	}

	prepared, err := prepareClaimedWorkspace(ctx, opts, profiles, runningPath, runDir, &loaded)
	if err != nil {
		return taskstate.FailMoveToStatus(opts.Root, runningPath, &loaded, err)
	}
	// Setup executor preflight runs after the worktree and input files are
	// prepared, before acceptance skeleton preflight, and before any executor
	// attempt. The daemon always delegates setup execution to the
	// setup executor (Claude or Codex per task.executor.cli); any
	// environment.setup plan is passed as model-visible context so the executor
	// can try, diagnose, and repair it before returning the successful plan for
	// Galley to persist. Setup readiness excludes
	// acceptance skeleton obligations.
	setupRes, setupUpdate, setupErr := setuppreflight.Run(ctx, setuppreflight.Options{
		Task:                   loaded,
		WorkDir:                prepared.CWD,
		RunDir:                 runDir,
		Profiles:               profiles,
		ClaudeBin:              opts.ClaudeBin,
		CodexBin:               opts.CodexBin,
		GLMAuthToken:           opts.GLMAuthToken,
		EnvironmentProfilePath: resolvedProfiles.EnvironmentProfileFile,
		ExecutorRunner:         opts.daemonDependencies().setupExecutorRunner,
	})
	if setupErr != nil {
		return failClaimedStage(opts.Root, runningPath, &loaded, setuppreflight.Phase, setuppreflight.FailedKind, setupErr, runDir)
	}
	// Apply setup readiness evidence (and any persisted profile change) to the
	// running task before the implementation work order is built so the
	// supervisor and executor share the same readiness facts.
	applySetupResultToTask(&loaded, setupRes, setupUpdate)
	if setupRes != nil {
		if err := task.Save(runningPath, loaded); err != nil {
			return failClaimedStage(opts.Root, runningPath, &loaded, setuppreflight.Phase, setuppreflight.FailedKind, err, runDir)
		}
	}
	// Optional acceptance skeleton preflight runs after inputfiles.Prepare and
	// before the first executor attempt. The stage is a no-op when the task
	// omits preflight.acceptance_skeleton.enabled or sets it to false. When the stage fails the daemon does not run the executor and
	// surfaces the failure through task status and run evidence.
	if cfg := loaded.Preflight; cfg != nil && cfg.AcceptanceSkeleton.IsEnabled() {
		res, perr := skeletonpreflight.Run(ctx, skeletonpreflight.Options{
			Task:         loaded,
			WorkDir:      prepared.CWD,
			RunDir:       runDir,
			Profiles:     profiles,
			ClaudeBin:    opts.ClaudeBin,
			CodexBin:     opts.CodexBin,
			GLMAuthToken: opts.GLMAuthToken,
		})
		if perr != nil {
			return failClaimedStage(opts.Root, runningPath, &loaded, "acceptance_skeleton_preflight", "acceptance_skeleton_preflight_failed", perr, runDir)
		}
		if res != nil {
			skeletonpreflight.ApplyToTask(&loaded, res)
			if err := task.Save(runningPath, loaded); err != nil {
				return failClaimedStage(opts.Root, runningPath, &loaded, "acceptance_skeleton_preflight", "acceptance_skeleton_preflight_failed", err, runDir)
			}
			if err := copyFile(runningPath, runartifact.Path(runDir, runartifact.EffectiveTaskSnapshotFilename)); err != nil {
				return failClaimedStage(opts.Root, runningPath, &loaded, "run_evidence", "run_evidence_failed", err, runDir)
			}
		}
	}
	return runSupervisorLoop(ctx, shutdownCtx, effectiveOpts, runningPath, &loaded, prepared, profiles, runDir, runID)
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

func prepareClaimedWorkspace(ctx context.Context, opts Options, profiles profile.Bundle, runningPath, runDir string, loaded *task.Task) (workspace.Prepared, error) {
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
		return workspace.Prepared{}, err
	}
	wsOpts.StartPoint = startPoint
	prepared, err := workspace.Prepare(ctx, loaded.Scope.CWD, loaded.Worktree, wsOpts)
	if err != nil {
		appendFailureAttempt(loaded, "workspace", "workspace_failed", err, runDir)
		return workspace.Prepared{}, err
	}
	if err := writeJSON(runartifact.Path(runDir, runartifact.WorkspaceFilename), prepared); err != nil {
		appendFailureAttempt(loaded, "run_evidence", "run_evidence_failed", err, runDir)
		return workspace.Prepared{}, err
	}
	if prepared.WorktreeReused {
		if err := reconcileReusedInputFiles(prepared.CWD, loaded.Files); err != nil {
			appendFailureAttempt(loaded, "input_files", "input_files_failed", err, runDir)
			return workspace.Prepared{}, err
		}
	}
	preparedFiles, err := inputfiles.Prepare(prepared.CWD, loaded.Files)
	if err != nil {
		appendFailureAttempt(loaded, "input_files", "input_files_failed", err, runDir)
		return workspace.Prepared{}, err
	}
	cleanupPrepared := true
	defer func() {
		if cleanupPrepared {
			_ = inputfiles.CleanupPrepared(preparedFiles)
		}
	}()
	if err := writeJSON(runartifact.Path(runDir, runartifact.InputFilesFilename), preparedFiles); err != nil {
		appendFailureAttempt(loaded, "run_evidence", "run_evidence_failed", err, runDir)
		return workspace.Prepared{}, err
	}
	if prepared.WorktreeReused && prepared.Dirty {
		appendRisk(loaded, "workspace-dirty", "technical_debt", "Reused worktree had uncommitted changes before executor run.", "Preserved existing worktree state and recorded git status porcelain in workspace.json.", true)
		if err := task.Save(runningPath, *loaded); err != nil {
			return workspace.Prepared{}, err
		}
	}
	if err := copyFile(runningPath, runartifact.Path(runDir, runartifact.EffectiveTaskSnapshotFilename)); err != nil {
		return workspace.Prepared{}, err
	}
	cleanupPrepared = false
	return prepared, nil
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

func executorStatus(result runner.RunResult, err error) string {
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
