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
	"github.com/shinpr/galley/internal/daemonconfig"
	"github.com/shinpr/galley/internal/daemonctl"
	"github.com/shinpr/galley/internal/galleyhome"
	"github.com/shinpr/galley/internal/runner"
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
					return
				}
				select {
				case <-signals:
					fmt.Fprintln(cmd.ErrOrStderr(), "galley: second shutdown signal received; exiting")
					os.Exit(130)
				case <-done:
				}
			}()
			if supervisorProvider != "" {
				switch supervisorProvider {
				case "codex", "claude":
					opts.Supervisor = supervisorProvider
					opts.SupervisorSource = daemon.SupervisorSourceCLI
				default:
					return fmt.Errorf("--supervisor must be one of: codex, claude")
				}
			}
			opts.Explicit = explicitOptionsFromFlags(cmd)
			if pollInterval > 0 {
				opts.PollInterval = pollInterval
			}
			// Daemon startup defaults. This RunE only fires
			// for `galley daemon run` (via the run subcommand) and for the
			// background daemon child spawned by `galley daemon start`
			// (parent cmd name "daemon"). The `status` and `stop`
			// subcommands install their own RunE and never reach this
			// branch, so they remain read-only and do not create
			// daemon.yaml. `galley daemon start` itself also calls
			// EnsureDefault before spawning the child so the operator sees
			// the new file as soon as the start command returns. We then
			// apply daemon.yaml values to any startup option the user did
			// not explicitly set on the CLI; CLI flags always win.
			if _, err := daemonconfig.EnsureDefault(opts.Root); err != nil {
				return err
			}
			if cfg, present, err := daemonconfig.Load(opts.Root); err != nil {
				return err
			} else if present {
				if err := applyDaemonConfig(&opts, &pollInterval, cfg); err != nil {
					return err
				}
			}
			daemonToken := os.Getenv(daemonctl.EnvToken)
			if daemonToken != "" {
				_ = os.Unsetenv(daemonctl.EnvToken)
			}
			if daemonToken != "" && pidFile == "" {
				return fmt.Errorf("%s requires --pid-file", daemonctl.EnvToken)
			}
			if daemonToken != "" {
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
				defer startPIDHeartbeat(pidFile, meta)()
				defer daemonctl.RemovePID(pidFile, os.Getpid())
			}
			err := daemon.Run(ctx, opts)
			close(done)
			return err
		},
	}

	flags := cmd.PersistentFlags()
	flags.StringVar(&opts.Root, "root", galleyhome.DefaultRoot(), "Galley daemon root directory")
	flags.BoolVar(&opts.Once, "once", false, "Process available queued tasks once and exit")
	flags.IntVar(&opts.MaxConcurrentTasks, "max-concurrent-tasks", 1, "Maximum concurrent tasks")
	flags.IntVar(&opts.MaxConcurrentPerRepo, "max-concurrent-per-repo", 1, "Maximum concurrent tasks per source repository; 0 disables the per-repo limit")
	flags.DurationVar(&pollInterval, "poll-interval", 10*time.Second, "Polling interval for non-once mode")
	flags.DurationVar(&opts.ClaimTTL, "claim-ttl", 30*time.Minute, "Recover running task and claim locks older than this duration")
	flags.DurationVar(&opts.HeartbeatInterval, "heartbeat-interval", 0, "Running task heartbeat interval; defaults to min(claim-ttl/4, 1m)")
	flags.DurationVar(&opts.ShutdownTimeout, "shutdown-timeout", 5*time.Minute, "After SIGINT/SIGTERM, let active attempts finish for this duration before canceling them")
	flags.DurationVar(&opts.IdleTimeout, "idle-timeout", 10*time.Minute, "Kill an executor or built-in supervisor subprocess that produces no stdout/stderr output for this duration")
	flags.StringVar(&supervisorProvider, "supervisor", "", fmt.Sprintf("Built-in supervisor adapter: claude or codex; defaults to %s", daemon.DefaultSupervisor))
	flags.StringVar(&pidFile, "pid-file", "", "PID file path for start, stop, and status; defaults to ROOT/galley-daemon.pid")
	flags.StringVar(&logFile, "log-file", "", "Log file path for start; defaults to ROOT/galley-daemon.log")
	flags.DurationVar(&stopTimeout, "stop-timeout", 30*time.Second, "How long stop waits after sending SIGTERM")
	flags.DurationVar(&readinessTimeout, "readiness-timeout", 750*time.Millisecond, "How long start waits to detect immediate daemon startup failure")
	cmd.AddCommand(
		newRunCommand(cmd),
		newStartCommand(&opts, &pidFile, &logFile, &readinessTimeout),
		newStopCommand(&opts, &pidFile, &stopTimeout),
		newStatusCommand(&opts, &pidFile),
	)

	return cmd
}

// applyDaemonConfig fills daemon startup options whose flag was not explicitly
// set with values from daemon.yaml. CLI flags always win. The supervisor
// source is recorded as `daemon_config` whenever daemon.yaml provided the
// resolved supervisor value, so run evidence can distinguish a CLI override
// from a daemon.yaml default. Per-task environment.yaml supervisor.default_cli
// overrides this later in the daemon runtime.
//
// When daemon.yaml supplies an integer concurrency value, the corresponding
// Explicit flag is set so daemon.Options.withDefaults treats it as an
// explicitly configured value. This preserves `max_concurrent_per_repo: 0`
// (disable the per-repo limit) from being silently turned back into 1 by the
// daemon default fallback.
func applyDaemonConfig(opts *daemon.Options, pollInterval *time.Duration, cfg daemonconfig.File) error {
	if !opts.Explicit.Supervisor && cfg.Supervisor != "" {
		opts.Supervisor = cfg.Supervisor
		opts.SupervisorSource = daemon.SupervisorSourceDaemonConfig
	}
	if !opts.Explicit.MaxConcurrentTasks && cfg.MaxConcurrentTasks != nil {
		opts.MaxConcurrentTasks = *cfg.MaxConcurrentTasks
		opts.Explicit.MaxConcurrentTasks = true
	}
	if !opts.Explicit.MaxConcurrentPerRepo && cfg.MaxConcurrentPerRepo != nil {
		opts.MaxConcurrentPerRepo = *cfg.MaxConcurrentPerRepo
		opts.Explicit.MaxConcurrentPerRepo = true
	}
	if !opts.Explicit.PollInterval && cfg.PollInterval != "" {
		if d, ok, err := daemonConfigDuration("poll_interval", cfg.PollInterval); err != nil {
			return err
		} else if ok {
			*pollInterval = d
			opts.PollInterval = d
		}
	}
	if !opts.Explicit.ClaimTTL && cfg.ClaimTTL != "" {
		if d, ok, err := daemonConfigDuration("claim_ttl", cfg.ClaimTTL); err != nil {
			return err
		} else if ok {
			opts.ClaimTTL = d
		}
	}
	if !opts.Explicit.HeartbeatInterval && cfg.HeartbeatInterval != "" {
		if d, ok, err := daemonConfigDuration("heartbeat_interval", cfg.HeartbeatInterval); err != nil {
			return err
		} else if ok {
			opts.HeartbeatInterval = d
		}
	}
	if !opts.Explicit.ShutdownTimeout && cfg.ShutdownTimeout != "" {
		if d, ok, err := daemonConfigDuration("shutdown_timeout", cfg.ShutdownTimeout); err != nil {
			return err
		} else if ok {
			opts.ShutdownTimeout = d
		}
	}
	if !opts.Explicit.IdleTimeout && cfg.IdleTimeout != "" {
		if d, ok, err := daemonConfigDuration("idle_timeout", cfg.IdleTimeout); err != nil {
			return err
		} else if ok {
			opts.IdleTimeout = d
		}
	}
	// Notifications come only from daemon.yaml; there is no CLI flag because the
	// hook is operator configuration. The block is already validated by
	// daemonconfig.Load before it reaches here.
	opts.Notifications = cfg.Notifications
	return nil
}

func daemonConfigDuration(field, value string) (time.Duration, bool, error) {
	d, ok, err := daemonconfig.Duration(value)
	if err != nil {
		return 0, false, fmt.Errorf("daemon.yaml %s: %w", field, err)
	}
	return d, ok, nil
}

func explicitOptionsFromFlags(cmd *cobra.Command) daemon.ExplicitOptions {
	changed := cmd.Flags().Changed
	return daemon.ExplicitOptions{
		Root:                 changed("root"),
		MaxConcurrentTasks:   changed("max-concurrent-tasks"),
		MaxConcurrentPerRepo: changed("max-concurrent-per-repo"),
		PollInterval:         changed("poll-interval"),
		ClaimTTL:             changed("claim-ttl"),
		HeartbeatInterval:    changed("heartbeat-interval"),
		ShutdownTimeout:      changed("shutdown-timeout"),
		IdleTimeout:          changed("idle-timeout"),
		Supervisor:           changed("supervisor"),
	}
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
			// Ensure daemon.yaml exists under the selected root before the
			// child daemon spawns. The background child cannot reliably
			// surface a creation error to the operator, and operators expect
			// to edit daemon.yaml immediately after `galley daemon start`
			// returns. The child still re-loads daemon.yaml so any value
			// written here is what the child resolves on first boot.
			if _, err := daemonconfig.EnsureDefault(opts.Root); err != nil {
				return err
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
			childArgs = append(childArgs, "--pid-file", paths.PIDFile)
			child := exec.Command(exe, childArgs...)
			child.Stdout = log
			child.Stderr = log
			child.Stdin = nil
			child.Env = append(os.Environ(), daemonctl.EnvToken+"="+token)
			configureBackgroundProcess(child)
			if err := child.Start(); err != nil {
				return err
			}
			meta := daemonctl.NewPIDFile(child.Process.Pid, exe, opts.Root, append([]string{exe}, childArgs...)).WithToken(token)
			if err := daemonctl.WritePID(paths.PIDFile, meta); err != nil {
				// Cross-platform start cleanup uses SIGTERM on Unix and
				// TerminateProcess on Windows so a PID write failure does
				// not leave a background daemon running.
				_ = daemonctl.TerminateChildProcess(child.Process)
				return err
			}
			if err := waitReady(paths.PIDFile, opts.Root, exe, *readinessTimeout); err != nil {
				// Same cross-platform start cleanup boundary as above: a
				// readiness timeout must actually terminate the child on
				// every OS, otherwise a slow-to-start daemon survives an
				// aborted `galley daemon start` and races a subsequent
				// retry against a stale process.
				_ = daemonctl.TerminateChildProcess(child.Process)
				_ = daemonctl.RemovePID(paths.PIDFile, child.Process.Pid)
				return fmt.Errorf("%w; see log file %s", err, paths.LogFile)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "galley daemon started pid=%d\npid_file=%s\nlog_file=%s\n", child.Process.Pid, paths.PIDFile, paths.LogFile)
			return child.Process.Release()
		},
	}
}

func newStopCommand(opts *daemon.Options, pidFile *string, stopTimeout *time.Duration) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
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
				// A stale daemon record means the daemon process is gone, but
				// it may have left behind executor/supervisor child process
				// groups it can no longer reap. Force stop must clean those up
				// before discarding the record; on cleanup failure surface the
				// surviving PIDs/PGIDs and keep the PID file so a follow-up
				// action can target the same daemon record.
				if err := cleanupOnForce(cmd, force, opts.Root, status.Meta.PID, *stopTimeout); err != nil {
					return err
				}
				if err := daemonctl.RemovePID(paths.PIDFile, status.Meta.PID); err != nil {
					return err
				}
				return daemonctl.ErrNotRunning
			}
			if !status.Verified {
				return fmt.Errorf("%w: pid=%d", daemonctl.ErrUnverifiedProcess, status.Meta.PID)
			}
			forced := false
			if force {
				wasForced, err := daemonctl.ForceStop(status.Meta, *stopTimeout)
				if err != nil && !errors.Is(err, daemonctl.ErrNotRunning) {
					return err
				}
				forced = wasForced
			} else if err := daemonctl.StopVerified(status.Meta, *stopTimeout); err != nil && !errors.Is(err, daemonctl.ErrNotRunning) {
				return err
			}
			// Force stop must clean up the daemon's known active child
			// process groups before reporting a stopped state or removing the
			// PID file. The daemon intentionally puts executor and supervisor
			// subprocesses into their own pgids, so killing only the daemon
			// PID can orphan them. On child cleanup failure we
			// surface a visible error that names the surviving PIDs/PGIDs and
			// intentionally leave the PID file in place so a follow-up
			// operator action can target the same daemon record instead of
			// observing a falsely-clean stop.
			if err := cleanupOnForce(cmd, force, opts.Root, status.Meta.PID, *stopTimeout); err != nil {
				return err
			}
			if err := daemonctl.RemovePID(paths.PIDFile, status.Meta.PID); err != nil {
				return err
			}
			if forced {
				fmt.Fprintf(cmd.OutOrStdout(), "galley daemon force stopped pid=%d\n", status.Meta.PID)
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "galley daemon stopped pid=%d\n", status.Meta.PID)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "After the stop timeout, send a verified SIGKILL to the daemon and to any known active executor or supervisor child process groups")
	return cmd
}

func cleanupOnForce(cmd *cobra.Command, force bool, root string, pid int, stopTimeout time.Duration) error {
	if !force {
		return nil
	}
	if _, err := daemonctl.CleanupRegisteredChildren(runner.ChildRegistryPath(root), stopTimeout); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "galley daemon force stop pid=%d incomplete: %v\n", pid, err)
		return err
	}
	return nil
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
	// Daemon status intentionally does not surface daemon startup
	// defaults that can be overridden by daemon.yaml or by per-repository
	// environment.yaml. `supervisor` is omitted because a daemon-wide value
	// would be misleading once per-task supervisor resolution exists.
	// `max_concurrent_tasks` and `max_concurrent_per_repo` are omitted for the
	// same reason: status can only see CLI argv, so daemon.yaml-supplied
	// values would not be reflected accurately. Resolution evidence for an
	// actual task is persisted in runs/<run-id>/supervisor.json.
	payload := struct {
		Running  bool   `json:"running"`
		Verified bool   `json:"verified"`
		PID      int    `json:"pid,omitempty"`
		Root     string `json:"root"`
		PIDFile  string `json:"pid_file"`
		LogFile  string `json:"log_file"`
	}{
		Running:  alive,
		Verified: verified,
		Root:     opts.Root,
		PIDFile:  paths.PIDFile,
		LogFile:  paths.LogFile,
	}
	if status != nil {
		payload.PID = status.Meta.PID
	}
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", " ")
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
	finished := make(chan struct{})
	go func() {
		defer close(finished)
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
		<-finished
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
