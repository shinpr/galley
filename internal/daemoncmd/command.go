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
	"strconv"
	"syscall"
	"time"

	"github.com/shinpr/galley/internal/daemon"
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
				default:
					return fmt.Errorf("--supervisor must be one of: codex, claude")
				}
			}
			opts.Explicit = explicitOptionsFromFlags(cmd)
			if pollInterval > 0 {
				opts.PollInterval = pollInterval
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
	flags.StringVar(&supervisorProvider, "supervisor", "", "Built-in supervisor adapter: claude or codex; defaults to claude")
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

func explicitOptionsFromFlags(cmd *cobra.Command) daemon.ExplicitOptions {
	changed := cmd.Flags().Changed
	return daemon.ExplicitOptions{
		Root:                 changed("root"),
		MaxConcurrentTasks:   changed("max-concurrent-tasks"),
		MaxConcurrentPerRepo: changed("max-concurrent-per-repo"),
		PollInterval:         changed("poll-interval"),
		ClaimTTL:             changed("claim-ttl"),
		HeartbeatInterval:    changed("heartbeat-interval"),
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
				// Cross-platform start cleanup: the previous SIGTERM call
				// was a no-op on Windows (the runtime returned "signal not
				// supported by windows") and left a stranded background
				// daemon after WritePID failures. The build-tagged helper
				// uses SIGTERM on Unix and TerminateProcess on Windows.
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
				// action can target the same daemon record (AC-005).
				if force {
					if _, err := daemonctl.CleanupRegisteredChildren(runner.ChildRegistryPath(opts.Root), *stopTimeout); err != nil {
						fmt.Fprintf(cmd.ErrOrStderr(), "galley daemon force stop pid=%d incomplete: %v\n", status.Meta.PID, err)
						return err
					}
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
			// PID can orphan them (D2 / AC-004). On child cleanup failure we
			// surface a visible error that names the surviving PIDs/PGIDs and
			// intentionally leave the PID file in place (AC-005) so a follow-up
			// operator action can target the same daemon record instead of
			// observing a falsely-clean stop.
			if force {
				if _, err := daemonctl.CleanupRegisteredChildren(runner.ChildRegistryPath(opts.Root), *stopTimeout); err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "galley daemon force stop pid=%d incomplete: %v\n", status.Meta.PID, err)
					return err
				}
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
	runtime := statusRuntimeFromOptions(*opts)
	if status != nil {
		runtime = runtime.withArgv(status.Meta.Argv)
	}
	payload := struct {
		Running           bool   `json:"running"`
		Verified          bool   `json:"verified"`
		PID               int    `json:"pid,omitempty"`
		Root              string `json:"root"`
		PIDFile           string `json:"pid_file"`
		LogFile           string `json:"log_file"`
		Supervisor        string `json:"supervisor,omitempty"`
		MaxConcurrent     int    `json:"max_concurrent_tasks,omitempty"`
		MaxConcurrentRepo int    `json:"max_concurrent_per_repo,omitempty"`
	}{
		Running:           alive,
		Verified:          verified,
		Root:              opts.Root,
		PIDFile:           paths.PIDFile,
		LogFile:           paths.LogFile,
		Supervisor:        runtime.Supervisor,
		MaxConcurrent:     runtime.MaxConcurrentTasks,
		MaxConcurrentRepo: runtime.MaxConcurrentPerRepo,
	}
	if status != nil {
		payload.PID = status.Meta.PID
	}
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	return enc.Encode(payload)
}

type statusRuntime struct {
	Supervisor           string
	MaxConcurrentTasks   int
	MaxConcurrentPerRepo int
}

func statusRuntimeFromOptions(opts daemon.Options) statusRuntime {
	return statusRuntime{
		Supervisor:           opts.Supervisor,
		MaxConcurrentTasks:   opts.MaxConcurrentTasks,
		MaxConcurrentPerRepo: opts.MaxConcurrentPerRepo,
	}
}

func (runtime statusRuntime) withArgv(argv []string) statusRuntime {
	if len(argv) == 0 {
		return runtime
	}
	if value, ok := flagValue(argv, "--supervisor"); ok {
		runtime.Supervisor = value
	} else if runtime.Supervisor == "" {
		runtime.Supervisor = "claude"
	}
	if value, ok := intFlagValue(argv, "--max-concurrent-tasks"); ok {
		runtime.MaxConcurrentTasks = value
	}
	if value, ok := intFlagValue(argv, "--max-concurrent-per-repo"); ok {
		runtime.MaxConcurrentPerRepo = value
	}
	return runtime
}

func flagValue(argv []string, name string) (string, bool) {
	for i, arg := range argv {
		if arg == name {
			if i+1 < len(argv) {
				return argv[i+1], true
			}
			return "", false
		}
		prefix := name + "="
		if len(arg) > len(prefix) && arg[:len(prefix)] == prefix {
			return arg[len(prefix):], true
		}
	}
	return "", false
}

func intFlagValue(argv []string, name string) (int, bool) {
	value, ok := flagValue(argv, name)
	if !ok {
		return 0, false
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, false
	}
	return parsed, true
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
