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
	"strings"
	"syscall"
	"time"

	"github.com/shinpr/galley/internal/daemon"
	"github.com/shinpr/galley/internal/daemonconfig"
	"github.com/shinpr/galley/internal/daemonctl"
	"github.com/shinpr/galley/internal/galleyhome"
	"github.com/shinpr/galley/internal/proc"
	"github.com/shinpr/galley/internal/provider"
	"github.com/spf13/cobra"
)

var (
	stopVerifiedForCommand            = daemonctl.StopVerified
	forceStopForCommand               = daemonctl.ForceStop
	afterInitialStopInspectForCommand func()
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
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()
			done := make(chan struct{})
			stopSignals := watchShutdownSignals(cmd, &opts, cancel, done)
			defer stopSignals()
			if err := applySupervisorFlag(&opts, supervisorProvider); err != nil {
				return err
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
			cfg, present, err := daemonconfig.Load(opts.Root)
			if err != nil {
				return err
			}
			if present {
				if err := applyDaemonConfig(&opts, &pollInterval, cfg); err != nil {
					return err
				}
			}
			releasePID, err := registerBackgroundDaemon(&opts, pidFile)
			if err != nil {
				return err
			}
			defer releasePID()
			err = daemon.Run(ctx, opts)
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
	flags.DurationVar(&opts.ClaimTTL, "claim-ttl", 30*time.Minute, "Recover running task and claim locks older than this duration; also sets heartbeat cadence to min(claim-ttl/4, 1m)")
	flags.DurationVar(&opts.ShutdownTimeout, "shutdown-timeout", 5*time.Minute, "After SIGINT/SIGTERM, let active attempts finish for this duration before canceling them")
	flags.DurationVar(&opts.IdleTimeout, "idle-timeout", 10*time.Minute, "Kill an executor or built-in supervisor subprocess that produces no stdout/stderr output for this duration")
	flags.StringVar(&supervisorProvider, "supervisor", "", fmt.Sprintf("Built-in supervisor adapter: %s; defaults to %s", strings.Join(provider.SupervisorIDs(), ", "), daemon.DefaultSupervisor))
	flags.StringVar(&pidFile, "pid-file", "", "PID file path for start, stop, and status; defaults to ROOT/galley-daemon.pid")
	flags.StringVar(&logFile, "log-file", "", "Log file path for start; defaults to ROOT/galley-daemon.log")
	flags.DurationVar(&stopTimeout, "stop-timeout", 30*time.Second, "How long stop waits after sending SIGTERM")
	flags.DurationVar(&readinessTimeout, "readiness-timeout", 750*time.Millisecond, "How long start waits to detect immediate daemon startup failure")
	cmd.AddCommand(
		newRunCommand(cmd),
		newStartCommand(&opts, &pidFile, &logFile, &readinessTimeout),
		newStopCommand(&opts, &pidFile, &stopTimeout),
		newStatusCommand(&opts, &pidFile),
		newConfigCommand(&opts),
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
	durations := []daemonConfigDurationField{
		{Name: "poll_interval", Value: cfg.PollInterval, Explicit: opts.Explicit.PollInterval, Apply: func(d time.Duration) {
			*pollInterval = d
			opts.PollInterval = d
		}},
		{Name: "claim_ttl", Value: cfg.ClaimTTL, Explicit: opts.Explicit.ClaimTTL, Apply: func(d time.Duration) { opts.ClaimTTL = d }},
		{Name: "shutdown_timeout", Value: cfg.ShutdownTimeout, Explicit: opts.Explicit.ShutdownTimeout, Apply: func(d time.Duration) { opts.ShutdownTimeout = d }},
		{Name: "idle_timeout", Value: cfg.IdleTimeout, Explicit: opts.Explicit.IdleTimeout, Apply: func(d time.Duration) { opts.IdleTimeout = d }},
	}
	for _, field := range durations {
		if err := field.apply(); err != nil {
			return err
		}
	}
	// Notifications come only from daemon.yaml; there is no CLI flag because the
	// hook is operator configuration. The block is already validated by
	// daemonconfig.Load before it reaches here.
	opts.Notifications = cfg.Notifications
	// Provider API keys are injected only into selected child environments.
	opts.GLMAuthToken = cfg.GLMAPIKey
	opts.KimiAPIKey = cfg.KimiAPIKey
	return nil
}

// daemonConfigDurationField is one daemon.yaml duration setting. A CLI flag
// that set the same option keeps precedence, so an explicit field is skipped.
type daemonConfigDurationField struct {
	Name     string
	Value    string
	Explicit bool
	Apply    func(time.Duration)
}

func (f daemonConfigDurationField) apply() error {
	if f.Explicit || f.Value == "" {
		return nil
	}
	d, ok, err := daemonConfigDuration(f.Name, f.Value)
	if err != nil {
		return err
	}
	if ok {
		f.Apply(d)
	}
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
		ShutdownTimeout:      changed("shutdown-timeout"),
		IdleTimeout:          changed("idle-timeout"),
		Supervisor:           changed("supervisor"),
	}
}

func newConfigCommand(opts *daemon.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "config",
		Short:         "Manage daemon.yaml configuration",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.AddCommand(newConfigInitCommand(opts))
	return cmd
}

func newConfigInitCommand(opts *daemon.Options) *cobra.Command {
	return &cobra.Command{
		Use:           "init",
		Short:         "Create daemon.yaml with documented defaults without starting the daemon",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// EnsureDefault is the sole daemon.yaml writer, so init creates the
			// same editable defaults as daemon startup and preserves existing files.
			created, err := daemonconfig.EnsureDefault(opts.Root)
			if err != nil {
				return err
			}
			path := daemonconfig.Path(opts.Root)
			if created {
				fmt.Fprintf(cmd.OutOrStdout(), "created %s\n", path)
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "%s already exists; leaving it unchanged\n", path)
			}
			return nil
		},
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
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runStartCommand(cmd, startRequest{Opts: opts, PIDFile: *pidFile, LogFile: *logFile, ReadinessTimeout: *readinessTimeout})
		},
	}
}

// startRequest is one `galley daemon start` invocation.
type startRequest struct {
	Opts             *daemon.Options
	PIDFile          string
	LogFile          string
	ReadinessTimeout time.Duration
}

func runStartCommand(cmd *cobra.Command, req startRequest) error {
	opts := req.Opts
	if opts.Once {
		return fmt.Errorf("start does not support --once")
	}
	// The background child cannot surface a creation error, so daemon.yaml is
	// created here; the child re-loads it and resolves what is written.
	if _, err := daemonconfig.EnsureDefault(opts.Root); err != nil {
		return err
	}
	paths := daemonctl.ResolvePaths(opts.Root, req.PIDFile, req.LogFile)
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve galley executable: %w", err)
	}
	release, err := daemonctl.ReservePID(paths.PIDFile)
	if err != nil {
		return err
	}
	defer release()
	if err := clearReplaceablePIDRecord(paths.PIDFile, opts.Root, exe); err != nil {
		return err
	}
	log, err := openDaemonLog(opts.Root, paths.LogFile)
	if err != nil {
		return err
	}
	defer func() { _ = log.Close() }()
	child, meta, err := spawnBackgroundDaemon(cmd, spawnPlan{Exe: exe, Root: opts.Root, PIDFile: paths.PIDFile, Log: log})
	if err != nil {
		return err
	}
	if err := daemonctl.WritePID(paths.PIDFile, meta); err != nil {
		// Cross-platform cleanup (SIGTERM on Unix, TerminateProcess on Windows)
		// so a PID write failure leaves no background daemon running.
		_ = daemonctl.TerminateChildProcess(child.Process)
		return err
	}
	if err := waitReady(paths.PIDFile, opts.Root, exe, req.ReadinessTimeout); err != nil {
		// Same cleanup boundary: a readiness timeout must terminate the child on
		// every OS, or a slow starter races the next `galley daemon start`.
		_ = daemonctl.TerminateChildProcess(child.Process)
		_ = daemonctl.RemovePID(paths.PIDFile, child.Process.Pid)
		return fmt.Errorf("%w; see log file %s", err, paths.LogFile)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "galley daemon started pid=%d\npid_file=%s\nlog_file=%s\n", child.Process.Pid, paths.PIDFile, paths.LogFile)
	if err := child.Process.Release(); err != nil {
		return fmt.Errorf("release background daemon process %d: %w", child.Process.Pid, err)
	}
	return nil
}

// clearReplaceablePIDRecord removes a stale PID record so a new daemon can take
// its place. A live daemon (verified or not) is never replaced.
func clearReplaceablePIDRecord(pidFile, root, exe string) error {
	status, err := daemonctl.Inspect(pidFile, root, exe)
	if err != nil {
		if errors.Is(err, daemonctl.ErrNotRunning) {
			return nil
		}
		return err
	}
	if status.Alive && status.Verified {
		return fmt.Errorf("galley daemon already running with pid %d", status.Meta.PID)
	}
	if status.Alive {
		return fmt.Errorf("refusing to replace pid file for unverified live pid %d", status.Meta.PID)
	}
	return daemonctl.RemovePID(pidFile, status.Meta.PID)
}

func openDaemonLog(root, logPath string) (*os.File, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create root: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return nil, fmt.Errorf("create log dir: %w", err)
	}
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open log file: %w", err)
	}
	return log, nil
}

// spawnPlan is the background daemon child this start command launches.
type spawnPlan struct {
	Exe     string
	Root    string
	PIDFile string
	Log     *os.File
}

func spawnBackgroundDaemon(cmd *cobra.Command, plan spawnPlan) (*exec.Cmd, daemonctl.PIDFile, error) {
	token, err := randomToken()
	if err != nil {
		return nil, daemonctl.PIDFile{}, err
	}
	childArgs := foregroundArgs(os.Args[1:], cmd.Name())
	childArgs = append(childArgs, "--pid-file", plan.PIDFile)
	//nolint:noctx // the background daemon must outlive this start command
	child := exec.Command(plan.Exe, childArgs...)
	child.Stdout = plan.Log
	child.Stderr = plan.Log
	child.Stdin = nil
	child.Env = append(os.Environ(), daemonctl.EnvToken+"="+token)
	configureBackgroundProcess(child)
	if err := child.Start(); err != nil {
		return nil, daemonctl.PIDFile{}, fmt.Errorf("start background daemon: %w", err)
	}
	meta := daemonctl.NewPIDFile(child.Process.Pid, plan.Exe, plan.Root, append([]string{plan.Exe}, childArgs...)).WithToken(token)
	return child, meta, nil
}

func newStopCommand(opts *daemon.Options, pidFile *string, stopTimeout *time.Duration) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:           "stop",
		Short:         "Stop a background Galley daemon",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runStopCommand(cmd, stopRequest{Opts: opts, PIDFile: *pidFile, StopTimeout: *stopTimeout, Force: force})
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "After the stop timeout, send a verified SIGKILL to the daemon and to any known active executor or supervisor child process groups")
	return cmd
}

// stopRequest is one `galley daemon stop` invocation.
type stopRequest struct {
	Opts        *daemon.Options
	PIDFile     string
	StopTimeout time.Duration
	Force       bool
}

// stopIntent is the stop-intent marker this invocation claimed. Only the leader
// signals the daemon; followers wait for the leader's stop to complete.
type stopIntent struct {
	Path   string
	Leader bool
}

// stopSession is one stop invocation bound to the daemon record it targets.
type stopSession struct {
	Req   stopRequest
	Paths daemonctl.Paths
	Exe   string
}

func runStopCommand(cmd *cobra.Command, req stopRequest) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve galley executable: %w", err)
	}
	session := stopSession{Req: req, Paths: daemonctl.ResolvePaths(req.Opts.Root, req.PIDFile, ""), Exe: exe}
	initial, err := daemonctl.Inspect(session.Paths.PIDFile, req.Opts.Root, exe)
	if errors.Is(err, daemonctl.ErrNotRunning) {
		return daemonctl.ErrNotRunning
	}
	if err != nil {
		return err
	}
	if afterInitialStopInspectForCommand != nil {
		afterInitialStopInspectForCommand()
	}
	var intent stopIntent
	if !req.Force {
		done, claimed, status, err := session.claimLeadership(cmd, initial)
		if err != nil || done {
			return err
		}
		intent, initial = claimed, status
	}
	if !initial.Alive {
		return session.stopStaleDaemon(cmd, initial, intent)
	}
	if !initial.Verified {
		if intent.Leader {
			removeStopIntent(intent.Path)
		}
		return fmt.Errorf("%w: pid=%d", daemonctl.ErrUnverifiedProcess, initial.Meta.PID)
	}
	return session.stopLiveDaemon(cmd, initial, intent)
}

// claimLeadership claims the stop intent and re-inspects the record. done means
// nothing is left: already stopped, identity changed, or the leader finished.
func (s stopSession) claimLeadership(cmd *cobra.Command, initial daemonctl.Status) (bool, stopIntent, daemonctl.Status, error) {
	intentPath, leader, err := claimStopIntent(s.Paths.PIDFile, initial.Meta)
	if err != nil {
		return false, stopIntent{}, initial, err
	}
	intent := stopIntent{Path: intentPath, Leader: leader}
	status, inspectErr := daemonctl.Inspect(s.Paths.PIDFile, s.Req.Opts.Root, s.Exe)
	if errors.Is(inspectErr, daemonctl.ErrNotRunning) || (inspectErr == nil && !sameDaemonIdentity(initial.Meta, status.Meta)) {
		removeStopIntent(intentPath)
		fmt.Fprintf(cmd.OutOrStdout(), "galley daemon stopped pid=%d\n", initial.Meta.PID)
		return true, intent, initial, nil
	}
	if inspectErr != nil {
		if leader {
			removeStopIntent(intentPath)
		}
		return false, intent, initial, inspectErr
	}
	if leader {
		return false, intent, status, nil
	}
	if err := waitForDaemonStop(stopWait{PIDFile: s.Paths.PIDFile, Root: s.Req.Opts.Root, Executable: s.Exe, Target: status.Meta, Timeout: s.Req.StopTimeout}); err != nil {
		return false, intent, status, err
	}
	removeStopIntent(intentPath)
	fmt.Fprintf(cmd.OutOrStdout(), "galley daemon stopped pid=%d\n", status.Meta.PID)
	return true, intent, status, nil
}

// stopStaleDaemon discards a record whose process is gone, force-cleaning the
// child groups first; on failure the PIDs surface and the PID file stays.
func (s stopSession) stopStaleDaemon(cmd *cobra.Command, status daemonctl.Status, intent stopIntent) error {
	if intent.Path != "" {
		removeStopIntent(intent.Path)
	}
	if err := cleanupOnForce(cmd, s.forceCleanupFor(status)); err != nil {
		return err
	}
	failedTasks := 0
	if s.Req.Force {
		var err error
		failedTasks, err = failOwnedRunningTasks(s.Req.Opts.Root, status.Meta)
		if err != nil {
			return err
		}
	}
	if err := daemonctl.RemovePID(s.Paths.PIDFile, status.Meta.PID); err != nil {
		return err
	}
	if s.Req.Force && failedTasks > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "galley daemon force stopped pid=%d\n", status.Meta.PID)
		return nil
	}
	return daemonctl.ErrNotRunning
}

// stopLiveDaemon signals a verified daemon and clears its record. Executor and
// supervisor subprocesses hold their own pgids, so force stop reaps them first.
func (s stopSession) stopLiveDaemon(cmd *cobra.Command, status daemonctl.Status, intent stopIntent) error {
	forced, intentPath, err := s.signalDaemon(status, intent)
	if err != nil {
		return err
	}
	// The stop intent is released only after the PID file is cleared so a
	// concurrent stop cannot observe a half-removed daemon record.
	if intentPath != "" {
		defer removeStopIntent(intentPath)
	}
	if err := cleanupOnForce(cmd, s.forceCleanupFor(status)); err != nil {
		return err
	}
	if s.Req.Force {
		if _, err := failOwnedRunningTasks(s.Req.Opts.Root, status.Meta); err != nil {
			return err
		}
	}
	if err := daemonctl.RemovePID(s.Paths.PIDFile, status.Meta.PID); err != nil {
		return err
	}
	if forced {
		fmt.Fprintf(cmd.OutOrStdout(), "galley daemon force stopped pid=%d\n", status.Meta.PID)
		return nil
	}
	fmt.Fprintf(cmd.OutOrStdout(), "galley daemon stopped pid=%d\n", status.Meta.PID)
	return nil
}

// signalDaemon stops the daemon and returns the stop-intent path to release
// once the record is cleared; an error keeps the intent for a follow-up stop.
func (s stopSession) signalDaemon(status daemonctl.Status, intent stopIntent) (forced bool, intentPath string, err error) {
	if s.Req.Force {
		wasForced, err := forceStopForCommand(status.Meta, s.Req.StopTimeout)
		if err != nil && !errors.Is(err, daemonctl.ErrNotRunning) {
			return false, "", err
		}
		return wasForced, stopIntentPath(s.Paths.PIDFile, status.Meta), nil
	}
	if err := stopVerifiedForCommand(status.Meta, s.Req.StopTimeout); err != nil && !errors.Is(err, daemonctl.ErrNotRunning) {
		return false, "", fmt.Errorf("shutdown remains in progress; normal stop will not signal again; use stop --force to recover, which interrupts active attempts: %w", err)
	}
	return false, intent.Path, nil
}

func (s stopSession) forceCleanupFor(status daemonctl.Status) forceCleanup {
	return forceCleanup{Force: s.Req.Force, Root: s.Req.Opts.Root, Owner: status.Meta, StopTimeout: s.Req.StopTimeout}
}

// forceCleanup is a force-stop's target daemon and its shutdown budget.
type forceCleanup struct {
	Force       bool
	Root        string
	Owner       daemonctl.PIDFile
	StopTimeout time.Duration
}

func cleanupOnForce(cmd *cobra.Command, req forceCleanup) error {
	force, root, owner, stopTimeout := req.Force, req.Root, req.Owner, req.StopTimeout
	if !force {
		return nil
	}
	if _, err := daemonctl.CleanupRegisteredChildren(proc.OwnedChildRegistryPath(root, owner.PID, owner.ProcessStartedAt), stopTimeout); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "galley daemon force stop pid=%d incomplete: %v\n", owner.PID, err)
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
		RunE: func(cmd *cobra.Command, _ []string) error {
			if output != "text" && output != "json" {
				return fmt.Errorf("unsupported output format %q", output)
			}
			paths := daemonctl.ResolvePaths(opts.Root, *pidFile, "")
			exe, err := os.Executable()
			if err != nil {
				return fmt.Errorf("resolve galley executable: %w", err)
			}
			status, err := daemonctl.Inspect(paths.PIDFile, opts.Root, exe)
			if errors.Is(err, daemonctl.ErrNotRunning) {
				if output == "json" {
					return writeStatusJSON(cmd, opts, paths, daemonLiveness{Status: nil, Alive: false, Verified: false})
				}
				fmt.Fprintln(cmd.OutOrStdout(), "galley daemon not running")
				return nil
			}
			if err != nil {
				return err
			}
			if !status.Alive {
				if output == "json" {
					return writeStatusJSON(cmd, opts, paths, daemonLiveness{Status: &status, Alive: false, Verified: status.Verified})
				}
				fmt.Fprintf(cmd.OutOrStdout(), "galley daemon not running stale_pid=%d\n", status.Meta.PID)
				return nil
			}
			if !status.Verified {
				if output == "json" {
					return writeStatusJSON(cmd, opts, paths, daemonLiveness{Status: &status, Alive: true, Verified: false})
				}
				fmt.Fprintf(cmd.OutOrStdout(), "galley daemon unverified pid=%d\n", status.Meta.PID)
				return nil
			}
			if output == "json" {
				return writeStatusJSON(cmd, opts, paths, daemonLiveness{Status: &status, Alive: true, Verified: true})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "galley daemon running pid=%d\n", status.Meta.PID)
			return nil
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", "text", "Output format: text or json")
	return cmd
}

// daemonLiveness is what the status probe concluded about the daemon.
type daemonLiveness struct {
	Status   *daemonctl.Status
	Alive    bool
	Verified bool
}

func writeStatusJSON(cmd *cobra.Command, opts *daemon.Options, paths daemonctl.Paths, live daemonLiveness) error {
	status, alive, verified := live.Status, live.Alive, live.Verified
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
	enc.SetIndent("", "  ")
	if err := enc.Encode(payload); err != nil {
		return fmt.Errorf("encode daemon status: %w", err)
	}
	return nil
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
		return "", fmt.Errorf("generate readiness token: %w", err)
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

// watchShutdownSignals cancels the daemon on the first SIGINT/SIGTERM and exits
// immediately on the second. The returned function stops the watch.
func watchShutdownSignals(cmd *cobra.Command, opts *daemon.Options, cancel context.CancelFunc, done <-chan struct{}) func() {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
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
	return func() { signal.Stop(signals) }
}

func applySupervisorFlag(opts *daemon.Options, supervisorProvider string) error {
	if supervisorProvider == "" {
		return nil
	}
	if !provider.IsSupervisor(supervisorProvider) {
		return fmt.Errorf("--supervisor must be one of: %s", strings.Join(provider.SupervisorIDs(), ", "))
	}
	opts.Supervisor = supervisorProvider
	opts.SupervisorSource = daemon.SupervisorSourceCLI
	return nil
}

// registerBackgroundDaemon claims the PID file when the one-shot environment
// token marks this process as the `galley daemon start` child; the result releases it.
func registerBackgroundDaemon(opts *daemon.Options, pidFile string) (func(), error) {
	daemonToken := os.Getenv(daemonctl.EnvToken)
	if daemonToken == "" {
		return func() {}, nil
	}
	_ = os.Unsetenv(daemonctl.EnvToken)
	if pidFile == "" {
		return nil, fmt.Errorf("%s requires --pid-file", daemonctl.EnvToken)
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve galley executable: %w", err)
	}
	resolved, err := daemon.Preflight(*opts)
	if err != nil {
		return nil, err
	}
	*opts = resolved
	meta := daemonctl.NewPIDFile(os.Getpid(), exe, opts.Root, os.Args).WithToken(daemonToken)
	if err := daemonctl.WritePID(pidFile, meta); err != nil {
		return nil, err
	}
	if err := daemonctl.Heartbeat(pidFile, meta); err != nil {
		return nil, err
	}
	stopHeartbeat := startPIDHeartbeat(pidFile, meta)
	return func() {
		stopHeartbeat()
		_ = daemonctl.RemovePID(pidFile, os.Getpid())
	}, nil
}
