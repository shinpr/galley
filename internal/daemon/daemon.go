package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/shinpr/galley/internal/config"
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
	PollInterval           time.Duration
	ClaimTTL               time.Duration
	HeartbeatInterval      time.Duration
	CommitOnAccept         bool
	OpenPR                 bool
	PollPRComments         bool
	ReplyPRComments        bool
	PRBase                 string
	SupervisorCommand      []string
	Explicit               ExplicitOptions
}

type ExplicitOptions struct {
	Root                   bool
	SystemPromptFile       bool
	JSONSchemaFile         bool
	QualityProfileFile     bool
	EnvironmentProfileFile bool
	MaxConcurrentTasks     bool
	PollInterval           bool
	ClaimTTL               bool
	HeartbeatInterval      bool
	CommitOnAccept         bool
	OpenPR                 bool
	PollPRComments         bool
	ReplyPRComments        bool
	PRBase                 bool
	SupervisorCommand      bool
}

var errClaimConflict = errors.New("claim conflict")

// Run starts the daemon loop.
func Run(ctx context.Context, opts Options) error {
	var err error
	opts, err = opts.withManifest()
	if err != nil {
		return err
	}
	opts = opts.withDefaults()
	if err := ensureLayout(opts.Root); err != nil {
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
				return err
			}
		}
		if _, err := processAvailable(ctx, opts); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
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
	if !opts.Explicit.PRBase {
		opts.PRBase = defaults.PRBase
	}
	if !opts.Explicit.SupervisorCommand {
		opts.SupervisorCommand = defaults.SupervisorCommand
	}
	return opts, nil
}

func (opts Options) withDefaults() Options {
	if opts.Root == "" {
		opts.Root = ".agent-workflow"
	}
	if opts.SystemPromptFile == "" {
		opts.SystemPromptFile = "prompts/claude-executor-full.md"
	}
	if opts.JSONSchemaFile == "" {
		opts.JSONSchemaFile = "schemas/claude-result.schema.json"
	}
	if opts.MaxConcurrentTasks <= 0 {
		opts.MaxConcurrentTasks = 1
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = 10 * time.Second
	}
	if opts.ClaimTTL <= 0 {
		opts.ClaimTTL = 30 * time.Minute
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
	if err := ensureLayout(opts.Root); err != nil {
		return 0, err
	}
	if err := recoverStaleClaims(opts.Root, opts.ClaimTTL, time.Now()); err != nil {
		return 0, err
	}
	queued, err := queuedTasks(opts.Root)
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
	claimedCount := 0
	var firstClaimErr error
	for _, queuedPath := range queued {
		if claimedCount >= limit {
			break
		}
		claimed, err := claimTask(opts.Root, queuedPath)
		if err != nil {
			if errors.Is(err, errClaimConflict) {
				continue
			}
			if firstClaimErr == nil {
				firstClaimErr = err
			}
			continue
		}
		claimedCount++
		wg.Add(1)
		go func(path string) {
			defer wg.Done()
			if err := processClaimedTask(ctx, opts, path); err != nil {
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

func ensureLayout(root string) error {
	for _, path := range []string{
		filepath.Join(root, "tasks", "queued"),
		filepath.Join(root, "tasks", "draft"),
		filepath.Join(root, "tasks", "ready"),
		filepath.Join(root, "tasks", "running"),
		filepath.Join(root, "tasks", "done"),
		filepath.Join(root, "tasks", "failed"),
		filepath.Join(root, "runs"),
	} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", path, err)
		}
	}
	return nil
}

func queuedTasks(root string) ([]string, error) {
	pattern := filepath.Join(root, "tasks", "queued", "*.yaml")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, err
	}
	sort.Strings(matches)
	return matches, nil
}

func claimTask(root, queuedPath string) (string, error) {
	runningPath := filepath.Join(root, "tasks", "running", filepath.Base(queuedPath))
	lockPath := runningPath + ".lock"
	lockFile, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return "", fmt.Errorf("%w: claim %s lock already exists at %s", errClaimConflict, queuedPath, lockPath)
		}
		return "", fmt.Errorf("reserve claim %s: %w", queuedPath, err)
	}
	defer os.Remove(lockPath)
	if err := lockFile.Close(); err != nil {
		return "", fmt.Errorf("reserve claim %s: %w", queuedPath, err)
	}
	if _, err := os.Stat(runningPath); err == nil {
		return "", fmt.Errorf("%w: running task already exists at %s", errClaimConflict, runningPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("inspect running task %s: %w", runningPath, err)
	}
	if err := os.Rename(queuedPath, runningPath); err != nil {
		return "", fmt.Errorf("claim %s: %w", queuedPath, err)
	}
	return runningPath, nil
}

func recoverStaleClaims(root string, ttl time.Duration, now time.Time) error {
	runningDir := filepath.Join(root, "tasks", "running")
	entries, err := os.ReadDir(runningDir)
	if err != nil {
		return fmt.Errorf("read running dir %s: %w", runningDir, err)
	}
	for _, entry := range entries {
		path := filepath.Join(runningDir, entry.Name())
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("stat %s: %w", path, err)
		}
		if now.Sub(info.ModTime()) < ttl {
			continue
		}
		if filepath.Ext(entry.Name()) == ".lock" {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("remove stale lock %s: %w", path, err)
			}
			continue
		}
		if entry.Type().IsRegular() && filepath.Ext(entry.Name()) == ".yaml" {
			if err := requeueRunningTask(root, path); err != nil {
				return err
			}
		}
	}
	return nil
}

func requeueRunningTask(root, runningPath string) error {
	loaded, err := task.Load(runningPath)
	if err != nil {
		return fmt.Errorf("load stale running task %s: %w", runningPath, err)
	}
	loaded.Status = "queued"
	if err := task.Save(runningPath, loaded); err != nil {
		return err
	}
	queuedPath := filepath.Join(root, "tasks", "queued", filepath.Base(runningPath))
	if err := noOverwriteRename(runningPath, queuedPath); err != nil {
		return fmt.Errorf("requeue stale task %s: %w", runningPath, err)
	}
	return nil
}

func noOverwriteRename(src, dst string) error {
	file, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%w: destination exists at %s", fs.ErrExist, dst)
		}
		return err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(dst)
		return err
	}
	if err := os.Rename(src, dst); err != nil {
		_ = os.Remove(dst)
		return err
	}
	return nil
}

func processClaimedTask(ctx context.Context, opts Options, runningPath string) error {
	loaded, err := task.Load(runningPath)
	if err != nil {
		_ = moveTask(opts.Root, runningPath, "failed", nil)
		return err
	}
	loaded.Status = "running"
	if err := task.Save(runningPath, loaded); err != nil {
		_ = moveTask(opts.Root, runningPath, "failed", nil)
		return err
	}
	stopHeartbeat := startHeartbeat(ctx, runningPath, opts.HeartbeatInterval)
	defer stopHeartbeat()

	runID := fmt.Sprintf("%s-%d", loaded.ID, time.Now().UnixNano())
	runDir := filepath.Join(opts.Root, "runs", runID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		_ = moveTask(opts.Root, runningPath, "failed", &loaded)
		return fmt.Errorf("create run dir %s: %w", runDir, err)
	}
	if err := copyFile(runningPath, filepath.Join(runDir, "task.yaml")); err != nil {
		_ = moveTask(opts.Root, runningPath, "failed", &loaded)
		return err
	}

	validation := task.Validate(loaded)
	if err := writeJSON(filepath.Join(runDir, "validation.json"), validation); err != nil {
		_ = moveTask(opts.Root, runningPath, "failed", &loaded)
		return err
	}
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
		_ = moveTask(opts.Root, runningPath, "failed", &loaded)
		return fmt.Errorf("task validation failed: %s", loaded.ID)
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
		_ = moveTask(opts.Root, runningPath, "failed", &loaded)
		return err
	}
	if err := writeJSON(filepath.Join(runDir, "workspace.json"), prepared); err != nil {
		loaded.Status = "failed"
		_ = moveTask(opts.Root, runningPath, "failed", &loaded)
		return err
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
			_ = moveTask(opts.Root, runningPath, "failed", &loaded)
			return err
		}
	}
	if err := copyFile(runningPath, filepath.Join(runDir, "task.effective.yaml")); err != nil {
		loaded.Status = "failed"
		_ = moveTask(opts.Root, runningPath, "failed", &loaded)
		return err
	}

	return runSupervisorLoop(ctx, opts, runningPath, &loaded, prepared, runDir, runID)
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
	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	defer file.Close()
	enc := json.NewEncoder(file)
	enc.SetIndent("", "  ")
	if err := enc.Encode(value); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("read %s: %w", src, err)
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", dst, err)
	}
	return nil
}
