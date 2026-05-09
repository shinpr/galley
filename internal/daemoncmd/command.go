package daemoncmd

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/shinpr/galley/internal/daemon"
	"github.com/shinpr/galley/internal/daemonctl"
	"github.com/shinpr/galley/internal/galleyhome"
	"github.com/spf13/cobra"
)

func NewCommand(use string) *cobra.Command {
	var opts daemon.Options
	var pollInterval time.Duration
	var supervisorProvider string
	var pidFile string
	var logFile string
	var stopTimeout time.Duration
	var readinessTimeout time.Duration
	var daemonToken string

	cmd := &cobra.Command{
		Use:           use,
		Short:         "Run the Galley file-backed task daemon",
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()
			signals := make(chan os.Signal, 1)
			signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
			defer signal.Stop(signals)
			done := make(chan struct{})
			go func() {
				select {
				case <-signals:
					fmt.Fprintf(cmd.ErrOrStderr(), "galley: shutdown requested; active attempts have up to %s to finish\n", opts.ShutdownTimeout)
					cancel()
				case <-done:
				}
			}()
			if supervisorProvider != "" {
				switch supervisorProvider {
				case "codex", "claude":
					opts.Supervisor = supervisorProvider
				default:
					return fmt.Errorf("--supervisor must be one of: codex, claude")
				}
			}
			opts.Explicit = daemon.ExplicitOptions{
				Root:                   cmd.Flags().Changed("root"),
				SystemPromptFile:       cmd.Flags().Changed("system-prompt-file"),
				JSONSchemaFile:         cmd.Flags().Changed("json-schema-file"),
				QualityProfileFile:     cmd.Flags().Changed("quality-profile-file"),
				EnvironmentProfileFile: cmd.Flags().Changed("environment-profile-file"),
				MaxConcurrentTasks:     cmd.Flags().Changed("max-concurrent-tasks"),
				MaxConcurrentPerRepo:   cmd.Flags().Changed("max-concurrent-per-repo"),
				PollInterval:           cmd.Flags().Changed("poll-interval"),
				ClaimTTL:               cmd.Flags().Changed("claim-ttl"),
				HeartbeatInterval:      cmd.Flags().Changed("heartbeat-interval"),
				CommitOnAccept:         cmd.Flags().Changed("commit-on-accept"),
				OpenPR:                 cmd.Flags().Changed("open-pr"),
				PollPRComments:         cmd.Flags().Changed("poll-pr-comments"),
				ReplyPRComments:        cmd.Flags().Changed("reply-pr-comments"),
				CleanupWorktrees:       cmd.Flags().Changed("cleanup-worktrees"),
				PRBase:                 cmd.Flags().Changed("pr-base"),
				Supervisor:             cmd.Flags().Changed("supervisor"),
			}
			if pollInterval > 0 {
				opts.PollInterval = pollInterval
			}
			stopPIDHeartbeat := func() {}
			if daemonToken != "" && pidFile != "" {
				exe, err := os.Executable()
				if err != nil {
					return err
				}
				resolved, err := daemon.Preflight(opts)
				if err != nil {
					return err
				}
				opts = resolved
				meta := daemonctl.NewPIDFile(os.Getpid(), exe, opts.Root, os.Args).WithToken(daemonToken)
				if err := daemonctl.WritePID(pidFile, meta); err != nil {
					return err
				}
				if err := daemonctl.Heartbeat(pidFile, meta); err != nil {
					return err
				}
				stopPIDHeartbeat = startPIDHeartbeat(pidFile, meta)
				defer stopPIDHeartbeat()
				defer daemonctl.RemovePID(pidFile, os.Getpid())
			}
			err := daemon.Run(ctx, opts)
			close(done)
			return err
		},
	}

	flags := cmd.PersistentFlags()
	flags.StringVar(&opts.Root, "root", galleyhome.DefaultRoot(), "Galley daemon root directory")
	flags.StringVar(&opts.ManifestFile, "manifest-file", "", "Optional Galley repo manifest YAML file")
	flags.StringVar(&opts.SystemPromptFile, "system-prompt-file", "", "Claude replacement system prompt file; defaults to the embedded Galley executor prompt")
	flags.StringVar(&opts.JSONSchemaFile, "json-schema-file", "", "Claude JSON schema file; defaults to the embedded Galley result schema")
	flags.StringVar(&opts.QualityProfileFile, "quality-profile-file", "", "Optional Galley quality profile YAML file")
	flags.StringVar(&opts.EnvironmentProfileFile, "environment-profile-file", "", "Optional Galley environment profile YAML file")
	flags.BoolVar(&opts.Once, "once", false, "Process available queued tasks once and exit")
	flags.IntVar(&opts.MaxConcurrentTasks, "max-concurrent-tasks", 1, "Maximum concurrent tasks")
	flags.IntVar(&opts.MaxConcurrentPerRepo, "max-concurrent-per-repo", 1, "Maximum concurrent tasks per source repository; 0 disables the per-repo limit")
	flags.DurationVar(&pollInterval, "poll-interval", 10*time.Second, "Polling interval for non-once mode")
	flags.DurationVar(&opts.ClaimTTL, "claim-ttl", 30*time.Minute, "Recover running task and claim locks older than this duration")
	flags.DurationVar(&opts.HeartbeatInterval, "heartbeat-interval", 0, "Running task heartbeat interval; defaults to min(claim-ttl/4, 1m)")
	flags.DurationVar(&opts.ShutdownTimeout, "shutdown-timeout", 5*time.Minute, "After SIGINT/SIGTERM, let active attempts finish for this duration before canceling them")
	flags.BoolVar(&opts.CommitOnAccept, "commit-on-accept", false, "Commit accepted worktree changes after executor completion")
	flags.BoolVar(&opts.OpenPR, "open-pr", false, "Commit, push, and create a pull request for accepted worktree changes")
	flags.BoolVar(&opts.PollPRComments, "poll-pr-comments", false, "Poll PR comments for /galley rerun commands and requeue matching tasks")
	flags.BoolVar(&opts.ReplyPRComments, "reply-pr-comments", false, "Post PR comments after handling /galley commands")
	flags.BoolVar(&opts.CleanupWorktrees, "cleanup-worktrees", false, "Remove clean worktrees for closed or merged PR tasks")
	flags.StringVar(&opts.PRBase, "pr-base", "", "Base branch for pull requests")
	flags.StringVar(&supervisorProvider, "supervisor", "", "Built-in supervisor adapter: claude or codex; defaults to claude")
	flags.BoolVar(&opts.DisableClaudeGuard, "disable-claude-guard", false, "Disable Galley's session-only Claude guard plugin")
	flags.StringVar(&opts.ClaudeGuardPluginDir, "claude-guard-plugin-dir", "", "Override Galley's generated Claude guard plugin directory")
	flags.StringVar(&pidFile, "pid-file", "", "PID file path for start, stop, and status; defaults to ROOT/galley-daemon.pid")
	flags.StringVar(&logFile, "log-file", "", "Log file path for start; defaults to ROOT/galley-daemon.log")
	flags.DurationVar(&stopTimeout, "stop-timeout", 30*time.Second, "How long stop waits after sending SIGTERM")
	flags.DurationVar(&readinessTimeout, "readiness-timeout", 750*time.Millisecond, "How long start waits to detect immediate daemon startup failure")
	flags.StringVar(&daemonToken, "daemon-token", "", "Internal daemon control token")
	_ = flags.MarkHidden("daemon-token")

	cmd.AddCommand(
		newRunCommand(cmd),
		newStartCommand(&opts, &pidFile, &logFile, &readinessTimeout),
		newStopCommand(&opts, &pidFile, &stopTimeout),
		newStatusCommand(&opts, &pidFile),
	)

	return cmd
}

func newRunCommand(parent *cobra.Command) *cobra.Command {
	return &cobra.Command{
		Use:           "run",
		Short:         "Run the Galley daemon in the foreground",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE:          parent.RunE,
	}
}

func newStartCommand(opts *daemon.Options, pidFile, logFile *string, readinessTimeout *time.Duration) *cobra.Command {
	return &cobra.Command{
		Use:           "start",
		Short:         "Start Galley daemon in the background",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if opts.Once {
				return fmt.Errorf("start does not support --once")
			}
			paths := daemonctl.ResolvePaths(opts.Root, *pidFile, *logFile)
			exe, err := os.Executable()
			if err != nil {
				return err
			}
			release, err := daemonctl.ReservePID(paths.PIDFile)
			if err != nil {
				return err
			}
			defer release()
			if status, err := daemonctl.Inspect(paths.PIDFile, opts.Root, exe); err == nil {
				if status.Alive && status.Verified {
					return fmt.Errorf("galley daemon already running with pid %d", status.Meta.PID)
				}
				if status.Alive && !status.Verified {
					return fmt.Errorf("refusing to replace pid file for unverified live pid %d", status.Meta.PID)
				}
				if !status.Alive {
					if err := daemonctl.RemovePID(paths.PIDFile, status.Meta.PID); err != nil {
						return err
					}
				}
			} else if !errors.Is(err, daemonctl.ErrNotRunning) {
				return err
			}
			if err := os.MkdirAll(opts.Root, 0o700); err != nil {
				return fmt.Errorf("create root: %w", err)
			}
			if err := os.MkdirAll(filepath.Dir(paths.LogFile), 0o700); err != nil {
				return fmt.Errorf("create log dir: %w", err)
			}
			log, err := os.OpenFile(paths.LogFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
			if err != nil {
				return fmt.Errorf("open log file: %w", err)
			}
			defer log.Close()
			token, err := randomToken()
			if err != nil {
				return err
			}
			childArgs := foregroundArgs(os.Args[1:], cmd.Name())
			childArgs = append(childArgs, "--pid-file", paths.PIDFile, "--daemon-token", token)
			child := exec.Command(exe, childArgs...)
			child.Stdout = log
			child.Stderr = log
			child.Stdin = nil
			child.Env = os.Environ()
			configureBackgroundProcess(child)
			if err := child.Start(); err != nil {
				return err
			}
			meta := daemonctl.NewPIDFile(child.Process.Pid, exe, opts.Root, append([]string{exe}, childArgs...)).WithToken(token)
			if err := daemonctl.WritePID(paths.PIDFile, meta); err != nil {
				_ = child.Process.Signal(syscall.SIGTERM)
				return err
			}
			if err := waitReady(paths.PIDFile, opts.Root, exe, *readinessTimeout); err != nil {
				_ = child.Process.Signal(syscall.SIGTERM)
				_ = daemonctl.RemovePID(paths.PIDFile, child.Process.Pid)
				return fmt.Errorf("%w; see log file %s", err, paths.LogFile)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "galley daemon started pid=%d\npid_file=%s\nlog_file=%s\n", child.Process.Pid, paths.PIDFile, paths.LogFile)
			return child.Process.Release()
		},
	}
}

func newStopCommand(opts *daemon.Options, pidFile *string, stopTimeout *time.Duration) *cobra.Command {
	return &cobra.Command{
		Use:           "stop",
		Short:         "Stop a background Galley daemon",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			paths := daemonctl.ResolvePaths(opts.Root, *pidFile, "")
			exe, err := os.Executable()
			if err != nil {
				return err
			}
			status, err := daemonctl.Inspect(paths.PIDFile, opts.Root, exe)
			if errors.Is(err, daemonctl.ErrNotRunning) {
				return daemonctl.ErrNotRunning
			}
			if err != nil {
				return err
			}
			if !status.Alive {
				if err := daemonctl.RemovePID(paths.PIDFile, status.Meta.PID); err != nil {
					return err
				}
				return daemonctl.ErrNotRunning
			}
			if !status.Verified {
				return fmt.Errorf("%w: pid=%d", daemonctl.ErrUnverifiedProcess, status.Meta.PID)
			}
			if err := daemonctl.StopVerified(status.Meta, *stopTimeout); err != nil && !errors.Is(err, daemonctl.ErrNotRunning) {
				return err
			}
			if err := daemonctl.RemovePID(paths.PIDFile, status.Meta.PID); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "galley daemon stopped pid=%d\n", status.Meta.PID)
			return nil
		},
	}
}

func newStatusCommand(opts *daemon.Options, pidFile *string) *cobra.Command {
	var output string
	cmd := &cobra.Command{
		Use:           "status",
		Short:         "Report background Galley daemon status",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			paths := daemonctl.ResolvePaths(opts.Root, *pidFile, "")
			exe, err := os.Executable()
			if err != nil {
				return err
			}
			status, err := daemonctl.Inspect(paths.PIDFile, opts.Root, exe)
			if errors.Is(err, daemonctl.ErrNotRunning) {
				if output == "json" {
					return writeStatusJSON(cmd, opts, paths, nil, false, false)
				}
				fmt.Fprintln(cmd.OutOrStdout(), "galley daemon not running")
				return nil
			}
			if err != nil {
				return err
			}
			if !status.Alive {
				if output == "json" {
					return writeStatusJSON(cmd, opts, paths, &status, false, status.Verified)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "galley daemon not running stale_pid=%d\n", status.Meta.PID)
				return nil
			}
			if !status.Verified {
				if output == "json" {
					return writeStatusJSON(cmd, opts, paths, &status, true, false)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "galley daemon unverified pid=%d\n", status.Meta.PID)
				return nil
			}
			if output == "json" {
				return writeStatusJSON(cmd, opts, paths, &status, true, true)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "galley daemon running pid=%d\n", status.Meta.PID)
			return nil
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", "text", "Output format: text or json")
	return cmd
}

func writeStatusJSON(cmd *cobra.Command, opts *daemon.Options, paths daemonctl.Paths, status *daemonctl.Status, alive, verified bool) error {
	payload := struct {
		Running           bool   `json:"running"`
		Verified          bool   `json:"verified"`
		PID               int    `json:"pid,omitempty"`
		Root              string `json:"root"`
		PIDFile           string `json:"pid_file"`
		LogFile           string `json:"log_file"`
		ManifestFile      string `json:"manifest_file,omitempty"`
		Supervisor        string `json:"supervisor,omitempty"`
		PollPRComments    bool   `json:"poll_pr_comments"`
		MaxConcurrent     int    `json:"max_concurrent_tasks,omitempty"`
		MaxConcurrentRepo int    `json:"max_concurrent_per_repo,omitempty"`
	}{
		Running:           alive,
		Verified:          verified,
		Root:              opts.Root,
		PIDFile:           paths.PIDFile,
		LogFile:           paths.LogFile,
		ManifestFile:      opts.ManifestFile,
		Supervisor:        opts.Supervisor,
		PollPRComments:    opts.PollPRComments,
		MaxConcurrent:     opts.MaxConcurrentTasks,
		MaxConcurrentRepo: opts.MaxConcurrentPerRepo,
	}
	if status != nil {
		payload.PID = status.Meta.PID
	}
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	return enc.Encode(payload)
}

func waitReady(pidFile, root, executable string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		status, err := daemonctl.Inspect(pidFile, root, executable)
		if err != nil {
			return err
		}
		if !status.Alive {
			return fmt.Errorf("galley daemon exited during startup")
		}
		if status.Verified {
			return nil
		}
		if timeout <= 0 || time.Now().After(deadline) {
			return fmt.Errorf("galley daemon did not become ready within %s", timeout)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func startPIDHeartbeat(pidFile string, meta daemonctl.PIDFile) func() {
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				_ = daemonctl.Heartbeat(pidFile, meta)
			}
		}
	}()
	return func() {
		close(done)
	}
}

func randomToken() (string, error) {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(data[:]), nil
}

func foregroundArgs(args []string, commandName string) []string {
	out := make([]string, 0, len(args))
	removed := false
	for _, arg := range args {
		if !removed && arg == commandName {
			removed = true
			continue
		}
		out = append(out, arg)
	}
	return out
}
