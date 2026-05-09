package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/shinpr/galley/internal/config"
	"github.com/shinpr/galley/internal/galleyhome"
	"github.com/shinpr/galley/internal/jsonio"
	"github.com/shinpr/galley/internal/queue"
	"github.com/shinpr/galley/internal/runner"
	"github.com/shinpr/galley/internal/task"
	"github.com/shinpr/galley/internal/workspace"
)

// Options configure the file-backed Galley daemon.
type Options struct {
	Root                   string
	ManifestFile           string
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
	CommitOnAccept         bool
	OpenPR                 bool
	PollPRComments         bool
	ReplyPRComments        bool
	CleanupWorktrees       bool
	PRBase                 string
	Supervisor             string
	ShutdownTimeout        time.Duration
	DisableClaudeGuard     bool
	ClaudeGuardPluginDir   string
	Explicit               ExplicitOptions
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
	CommitOnAccept         bool
	OpenPR                 bool
	PollPRComments         bool
	ReplyPRComments        bool
	CleanupWorktrees       bool
	PRBase                 bool
	Supervisor             bool
}

// Run starts the daemon loop.
func Run(ctx context.Context, opts Options) error {
	var err error
	opts, err = Preflight(opts)
	if err != nil {
		return err
	}
	if opts.Once {
		var firstErr error
		for {
			if opts.PollPRComments {
				if err := pollPRComments(ctx, opts); err != nil && firstErr == nil {
					firstErr = err
				}
			}
			if opts.CleanupWorktrees {
				if err := cleanupWorktrees(ctx, opts); err != nil && firstErr == nil {
					firstErr = err
				}
			}
			processed, err := processAvailable(ctx, opts)
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
			}
			if processed == 0 {
				return firstErr
			}
		}
	}

	ticker := time.NewTicker(opts.PollInterval)
	defer ticker.Stop()
	for {
		if opts.PollPRComments {
			if err := pollPRComments(ctx, opts); err != nil {
				fmt.Fprintf(os.Stderr, "galley: poll PR comments failed: %v\n", err)
			}
		}
		if opts.CleanupWorktrees {
			if err := cleanupWorktrees(ctx, opts); err != nil {
				fmt.Fprintf(os.Stderr, "galley: cleanup worktrees failed: %v\n", err)
			}
		}
		if _, err := processAvailable(ctx, opts); err != nil {
			fmt.Fprintf(os.Stderr, "galley: process available tasks failed: %v\n", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// Preflight resolves daemon options and verifies startup prerequisites.
func Preflight(opts Options) (Options, error) {
	var err error
	opts, err = opts.withManifest()
	if err != nil {
		return Options{}, err
	}
	opts = opts.withDefaults()
	if opts.Supervisor != "" && opts.Supervisor != "codex" && opts.Supervisor != "claude" {
		return Options{}, fmt.Errorf("supervisor must be one of: codex, claude")
	}
	if err := queue.EnsureLayout(opts.Root); err != nil {
		return Options{}, err
	}
	return opts, nil
}

func (opts Options) withManifest() (Options, error) {
	if opts.ManifestFile == "" {
		return opts, nil
	}
	manifest, err := config.LoadManifest(opts.ManifestFile)
	if err != nil {
		return Options{}, err
	}
	defaults := manifest.Defaults
	if !opts.Explicit.SystemPromptFile {
		opts.SystemPromptFile = defaults.SystemPromptFile
	}
	if !opts.Explicit.JSONSchemaFile {
		opts.JSONSchemaFile = defaults.JSONSchemaFile
	}
	if !opts.Explicit.QualityProfileFile {
		opts.QualityProfileFile = defaults.QualityProfileFile
	}
	if !opts.Explicit.EnvironmentProfileFile {
		opts.EnvironmentProfileFile = defaults.EnvironmentProfileFile
	}
	if !opts.Explicit.MaxConcurrentTasks {
		opts.MaxConcurrentTasks = defaults.MaxConcurrentTasks
	}
	if !opts.Explicit.MaxConcurrentPerRepo {
		opts.MaxConcurrentPerRepo = defaults.MaxConcurrentPerRepo
	}
	if !opts.Explicit.PollInterval {
		opts.PollInterval = defaults.PollInterval
	}
	if !opts.Explicit.ClaimTTL {
		opts.ClaimTTL = defaults.ClaimTTL
	}
	if !opts.Explicit.HeartbeatInterval {
		opts.HeartbeatInterval = defaults.HeartbeatInterval
	}
	if !opts.Explicit.CommitOnAccept {
		opts.CommitOnAccept = defaults.CommitOnAccept
	}
	if !opts.Explicit.OpenPR {
		opts.OpenPR = defaults.OpenPR
	}
	if !opts.Explicit.PollPRComments {
		opts.PollPRComments = defaults.PollPRComments
	}
	if !opts.Explicit.ReplyPRComments {
		opts.ReplyPRComments = defaults.ReplyPRComments
	}
	if !opts.Explicit.CleanupWorktrees {
		opts.CleanupWorktrees = defaults.CleanupWorktrees
	}
	if !opts.Explicit.PRBase {
		opts.PRBase = defaults.PRBase
	}
	if !opts.Explicit.Supervisor {
		opts.Supervisor = defaults.Supervisor
	}
	return opts, nil
}

func (opts Options) withDefaults() Options {
	if opts.Root == "" {
		opts.Root = galleyhome.DefaultRoot()
	}
	if opts.Supervisor == "" {
		opts.Supervisor = "claude"
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
	if opts.HeartbeatInterval <= 0 {
		opts.HeartbeatInterval = opts.ClaimTTL / 4
		if opts.HeartbeatInterval <= 0 {
			opts.HeartbeatInterval = time.Minute
		}
		if opts.HeartbeatInterval > time.Minute {
			opts.HeartbeatInterval = time.Minute
		}
	}
	if opts.OpenPR {
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
	claimedCount := 0
	var firstClaimErr error
	repoCounts := queue.RunningRepoCounts(opts.Root)
	stopClaiming := false
	for _, queuedPath := range queued {
		if claimedCount >= limit || stopClaiming {
			break
		}
		select {
		case <-ctx.Done():
			if firstClaimErr == nil {
				firstClaimErr = ctx.Err()
			}
			stopClaiming = true
			continue
		default:
		}
		repoKey := ""
		var repoLoadErr error
		if queuedHasClaimConflict(opts.Root, queuedPath) {
			continue
		}
		if opts.MaxConcurrentPerRepo > 0 {
			loaded, loadErr := task.Load(queuedPath)
			if loadErr != nil {
				repoLoadErr = loadErr
				if firstClaimErr == nil {
					firstClaimErr = fmt.Errorf("load queued task for repo limit %s: %w", queuedPath, repoLoadErr)
				}
				continue
			} else {
				repoKey = loaded.Scope.CWD
				if repoKey != "" && repoCounts[repoKey] >= opts.MaxConcurrentPerRepo {
					continue
				}
			}
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
		if repoKey != "" {
			repoCounts[repoKey]++
		}
		wg.Add(1)
		go func(path string) {
			defer wg.Done()
			if err := processClaimedTask(taskCtx, ctx, opts, path); err != nil {
				errs <- err
			}
		}(claimed)
	}
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

func queuedHasClaimConflict(root, queuedPath string) bool {
	runningPath := filepath.Join(root, "tasks", "running", filepath.Base(queuedPath))
	if _, err := os.Stat(runningPath); err == nil {
		return true
	}
	if _, err := os.Stat(runningPath + ".lock"); err == nil {
		return true
	}
	return false
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
	loaded, err := task.Load(runningPath)
	if err != nil {
		return failTaskMove(opts.Root, runningPath, nil, err)
	}
	task.ResolveFileSources(runningPath, &loaded)
	task.ApplyDefaults(&loaded)
	loaded.Status = "running"
	if err := task.Save(runningPath, loaded); err != nil {
		return failTaskMove(opts.Root, runningPath, nil, err)
	}
	stopHeartbeat := startHeartbeat(ctx, runningPath, opts.HeartbeatInterval)
	defer stopHeartbeat()

	validation := task.Validate(loaded)
	if !validation.Valid() {
		loaded.Status = "failed"
		loaded.Attempts = append(loaded.Attempts, task.Attempt{
			Number:            len(loaded.Attempts) + 1,
			StartedAt:         time.Now().UTC().Format(time.RFC3339Nano),
			CompletedAt:       time.Now().UTC().Format(time.RFC3339Nano),
			ClaudeStatus:      "not_run",
			SupervisorVerdict: "validation_failed",
			Summary:           "Task validation failed before executor run.",
		})
		return failTaskMove(opts.Root, runningPath, &loaded, fmt.Errorf("task validation failed: %v", validation.Errors))
	}

	runID := fmt.Sprintf("%s-%d", loaded.ID, time.Now().UnixNano())
	runDir := filepath.Join(opts.Root, "runs", runID)
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		return failTaskMove(opts.Root, runningPath, &loaded, fmt.Errorf("create run dir %s: %w", runDir, err))
	}
	if err := copyFile(runningPath, filepath.Join(runDir, "task.yaml")); err != nil {
		return failTaskMove(opts.Root, runningPath, &loaded, err)
	}

	if err := writeJSON(filepath.Join(runDir, "validation.json"), validation); err != nil {
		return failTaskMove(opts.Root, runningPath, &loaded, err)
	}

	prepared, err := workspace.Prepare(ctx, loaded.Scope.CWD, loaded.Worktree)
	if err != nil {
		loaded.Status = "failed"
		loaded.Attempts = append(loaded.Attempts, task.Attempt{
			Number:            len(loaded.Attempts) + 1,
			StartedAt:         time.Now().UTC().Format(time.RFC3339Nano),
			CompletedAt:       time.Now().UTC().Format(time.RFC3339Nano),
			ClaudeStatus:      "not_run",
			SupervisorVerdict: "workspace_failed",
			Summary:           err.Error(),
		})
		return failTaskMove(opts.Root, runningPath, &loaded, err)
	}
	if err := writeJSON(filepath.Join(runDir, "workspace.json"), prepared); err != nil {
		loaded.Status = "failed"
		return failTaskMove(opts.Root, runningPath, &loaded, err)
	}
	preparedFiles, err := prepareInputFiles(prepared.CWD, loaded.Files)
	if err != nil {
		loaded.Status = "failed"
		return failTaskMove(opts.Root, runningPath, &loaded, err)
	}
	if err := writeJSON(filepath.Join(runDir, "input_files.json"), preparedFiles); err != nil {
		loaded.Status = "failed"
		return failTaskMove(opts.Root, runningPath, &loaded, err)
	}
	if prepared.WorktreeReused && prepared.Dirty {
		loaded.Risks = append(loaded.Risks, task.Risk{
			ID:                   fmt.Sprintf("workspace-dirty-%d", len(loaded.Risks)+1),
			Type:                 "technical_debt",
			Detail:               "Reused worktree had uncommitted changes before executor run.",
			Mitigation:           "Preserved existing worktree state and recorded git status porcelain in workspace.json.",
			HumanReviewSuggested: true,
		})
		if err := task.Save(runningPath, loaded); err != nil {
			loaded.Status = "failed"
			return failTaskMove(opts.Root, runningPath, &loaded, err)
		}
	}
	if err := copyFile(runningPath, filepath.Join(runDir, "task.effective.yaml")); err != nil {
		loaded.Status = "failed"
		return failTaskMove(opts.Root, runningPath, &loaded, err)
	}

	return runSupervisorLoop(ctx, shutdownCtx, opts, runningPath, &loaded, prepared, runDir, runID)
}

func startHeartbeat(ctx context.Context, path string, interval time.Duration) func() {
	heartbeatCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	touch := func() {
		now := time.Now()
		_ = os.Chtimes(path, now, now)
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

func moveTask(root, currentPath, state string, updated *task.Task) error {
	if updated != nil {
		if err := task.Save(currentPath, *updated); err != nil {
			return err
		}
	}
	nextPath := filepath.Join(root, "tasks", state, filepath.Base(currentPath))
	if err := os.Rename(currentPath, nextPath); err != nil {
		return fmt.Errorf("move task to %s: %w", state, err)
	}
	return nil
}

func failTaskMove(root, runningPath string, updated *task.Task, primary error) error {
	if updated != nil && (updated.Status == "" || updated.Status == "queued" || updated.Status == "running") {
		updated.Status = "failed"
	}
	if moveErr := moveTask(root, runningPath, "failed", updated); moveErr != nil {
		fmt.Fprintf(os.Stderr, "galley: failed to move task %s to failed: %v (primary: %v)\n", runningPath, moveErr, primary)
		return errors.Join(primary, moveErr)
	}
	return primary
}

func claudeStatus(result runner.RunResult, err error) string {
	if err == nil {
		return "completed"
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
