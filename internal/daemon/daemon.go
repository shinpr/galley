package daemon

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/shinpr/galley/internal/galleyhome"
	"github.com/shinpr/galley/internal/inputfiles"
	"github.com/shinpr/galley/internal/jsonio"
	"github.com/shinpr/galley/internal/profile"
	"github.com/shinpr/galley/internal/queue"
	"github.com/shinpr/galley/internal/runner"
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
	ClaudeBin              string
	CodexBin               string
	GitBin                 string
	GHBin                  string
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
			processed, err := processQueuedTasks(ctx, opts)
			if err != nil && firstErr == nil {
				firstErr = err
			}
			if processed == 0 {
				return firstErr
			}
		}
	}

	ticker := time.NewTicker(opts.PollInterval)
	defer ticker.Stop()
	for {
		if _, err := runDaemonCycle(ctx, opts); err != nil {
			fmt.Fprintf(os.Stderr, "galley: iteration failed: %v\n", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func runDaemonCycle(ctx context.Context, opts Options) (int, error) {
	var errs []error
	if err := pollPRComments(ctx, opts); err != nil {
		errs = append(errs, fmt.Errorf("poll PR comments: %w", err))
	}
	if err := cleanupWorktrees(ctx, opts); err != nil {
		errs = append(errs, fmt.Errorf("cleanup worktrees: %w", err))
	}
	processed, err := processQueuedTasks(ctx, opts)
	if err != nil {
		errs = append(errs, err)
	}
	return processed, errors.Join(errs...)
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
	if opts.Supervisor != "" && opts.Supervisor != "codex" && opts.Supervisor != "claude" {
		return Options{}, fmt.Errorf("supervisor must be one of: codex, claude")
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
	repoKey := loaded.Scope.CWD
	if repoKey != "" && repoCounts[repoKey] >= opts.MaxConcurrentPerRepo {
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
	runningPath := filepath.Join(root, "tasks", "running", filepath.Base(queuedPath))
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
	loaded, err := loadClaimedTask(runningPath)
	if err != nil {
		return taskstate.FailMove(opts.Root, runningPath, nil, err)
	}
	stopHeartbeat := startHeartbeat(ctx, runningPath, opts.HeartbeatInterval)
	defer stopHeartbeat()

	validation, err := validateClaimedTask(&loaded)
	if err != nil {
		return taskstate.FailMove(opts.Root, runningPath, &loaded, err)
	}
	runID, runDir, err := initializeRunEvidence(opts.Root, runningPath, loaded, validation)
	if err != nil {
		appendFailureAttempt(&loaded, "run_evidence", "run_evidence_failed", err, "")
		return taskstate.FailMove(opts.Root, runningPath, &loaded, err)
	}

	// Profile resolution must happen before workspace.Prepare so that the
	// resolved environment profile (specifically pr.base) can supply a
	// start-point ref to the brand-new task branch instead of inheriting
	// the source repository's current HEAD. The resolved bundle is threaded
	// into runSupervisorLoop so the supervisor loop never re-loads it.
	profiles, err := loadAndPersistTaskProfiles(opts, &loaded, runDir)
	if err != nil {
		appendFailureAttempt(&loaded, "run_evidence", "run_evidence_failed", err, runDir)
		return taskstate.FailMove(opts.Root, runningPath, &loaded, err)
	}

	prepared, err := prepareClaimedWorkspace(ctx, opts, profiles, runningPath, runDir, &loaded)
	if err != nil {
		return taskstate.FailMove(opts.Root, runningPath, &loaded, err)
	}
	return runSupervisorLoop(ctx, shutdownCtx, opts, runningPath, &loaded, prepared, profiles, runDir, runID)
}

// loadAndPersistTaskProfiles resolves the quality and environment profiles for
// the claimed task and writes the run evidence file (profiles.json) into the
// run directory. The same shape that loop.go's loadSupervisorProfiles wrote
// previously is preserved so existing readers of profiles.json continue to
// work.
func loadAndPersistTaskProfiles(opts Options, loaded *task.Task, runDir string) (profile.Bundle, error) {
	resolved, profiles, err := loadTaskProfiles(opts, loaded.Scope.CWD)
	if err != nil {
		return profile.Bundle{}, err
	}
	if err := writeJSON(filepath.Join(runDir, "profiles.json"), struct {
		Resolved resolvedProfileFiles `json:"resolved"`
		Bundle   profile.Bundle       `json:"bundle"`
	}{Resolved: resolved, Bundle: profiles}); err != nil {
		return profile.Bundle{}, err
	}
	return profiles, nil
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
	if err := copyFile(runningPath, filepath.Join(runDir, "task.yaml")); err != nil {
		return "", "", err
	}
	evidence := newValidationEvidence(loaded, validation, time.Now())
	if err := writeJSON(filepath.Join(runDir, "validation.json"), evidence); err != nil {
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
	if err := writeJSON(filepath.Join(runDir, "workspace.json"), prepared); err != nil {
		appendFailureAttempt(loaded, "run_evidence", "run_evidence_failed", err, runDir)
		return workspace.Prepared{}, err
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
	if err := writeJSON(filepath.Join(runDir, "input_files.json"), preparedFiles); err != nil {
		appendFailureAttempt(loaded, "run_evidence", "run_evidence_failed", err, runDir)
		return workspace.Prepared{}, err
	}
	if prepared.WorktreeReused && prepared.Dirty {
		loaded.Risks = append(loaded.Risks, task.Risk{
			ID:                   fmt.Sprintf("workspace-dirty-%d", len(loaded.Risks)+1),
			Type:                 "technical_debt",
			Detail:               "Reused worktree had uncommitted changes before executor run.",
			Mitigation:           "Preserved existing worktree state and recorded git status porcelain in workspace.json.",
			HumanReviewSuggested: true,
		})
		if err := task.Save(runningPath, *loaded); err != nil {
			return workspace.Prepared{}, err
		}
	}
	if err := copyFile(runningPath, filepath.Join(runDir, "task.effective.yaml")); err != nil {
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
