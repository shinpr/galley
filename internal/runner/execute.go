package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultTailBytes       = 64 * 1024
	processCancelWaitLimit = 5 * time.Second
	minIdleCheckInterval   = time.Second
	maxIdleCheckInterval   = 30 * time.Second
)

var (
	ErrIdleTimeout = errors.New("idle timeout")
	ErrTimeout     = errors.New("timeout")
	ErrKilled      = errors.New("killed")
	ErrExitNonZero = errors.New("exit nonzero")
)

type CommandErrorKind string

const (
	CommandErrorIdleTimeout CommandErrorKind = "idle_timeout"
	CommandErrorTimeout     CommandErrorKind = "timeout"
	CommandErrorKilled      CommandErrorKind = "killed"
	CommandErrorExitNonZero CommandErrorKind = "exit_nonzero"
	CommandErrorStart       CommandErrorKind = "start_failed"
)

type CommandError struct {
	Kind   CommandErrorKind
	Result RunResult
	Err    error
}

func (e *CommandError) Error() string {
	if e == nil || e.Err == nil {
		return string(e.Kind)
	}
	return e.Err.Error()
}

func (e *CommandError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func (e *CommandError) Is(target error) bool {
	if e == nil {
		return false
	}
	switch target {
	case ErrIdleTimeout:
		return e.Kind == CommandErrorIdleTimeout
	case ErrTimeout:
		return e.Kind == CommandErrorTimeout
	case ErrKilled:
		return e.Kind == CommandErrorKilled
	case ErrExitNonZero:
		return e.Kind == CommandErrorExitNonZero
	default:
		return false
	}
}

// RunOptions controls subprocess execution and optional audit file capture.
type RunOptions struct {
	Timeout    time.Duration
	StdoutPath string
	StderrPath string
	// TailBytes controls in-memory stdout/stderr tail size. Zero uses the default; negative keeps all output.
	TailBytes int
	// IdleTimeout kills the subprocess process group when stdout/stderr produces
	// no new output for this duration. Zero or negative disables the watchdog.
	// It is independent of Timeout, which bounds total wall-clock duration.
	IdleTimeout time.Duration
}

// RunResult reports the observable result of a subprocess run.
type RunResult struct {
	ExitCode int           `json:"exit_code"`
	Stdout   string        `json:"stdout,omitempty"`
	Stderr   string        `json:"stderr,omitempty"`
	Duration time.Duration `json:"duration"`
	TimedOut bool          `json:"timed_out"`
	// IdleTimedOut reports that the idle-output watchdog terminated the process
	// because it produced no stdout/stderr for RunOptions.IdleTimeout.
	IdleTimedOut bool `json:"idle_timed_out,omitempty"`
}

// RunCommand executes a command plan without going through a shell.
func RunCommand(ctx context.Context, command Command, opts RunOptions) (RunResult, error) {
	started := time.Now()
	if len(command.Argv) == 0 {
		return RunResult{}, fmt.Errorf("argv is empty")
	}

	runCtx := ctx
	var cancel context.CancelFunc
	if opts.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}

	cmd := exec.CommandContext(runCtx, command.Argv[0], command.Argv[1:]...)
	cmd.SysProcAttr = processGroupAttr()
	if command.WorkDir != "" {
		cmd.Dir = command.WorkDir
	}
	if len(command.EnvAppend) > 0 {
		cmd.Env = append(os.Environ(), command.EnvAppend...)
	}
	if command.Stdin != "" {
		cmd.Stdin = strings.NewReader(command.Stdin)
	}

	tailBytes := opts.TailBytes
	if tailBytes == 0 {
		tailBytes = defaultTailBytes
	}
	stdout := newTailBuffer(tailBytes)
	stderr := newTailBuffer(tailBytes)
	stdoutWriter, stdoutFile, err := captureWriter(stdout, opts.StdoutPath)
	if err != nil {
		return RunResult{}, err
	}
	stderrWriter, stderrFile, err := captureWriter(stderr, opts.StderrPath)
	if err != nil {
		_ = stdoutFile.Close()
		return RunResult{}, err
	}

	var lastActivity atomic.Int64
	lastActivity.Store(time.Now().UnixNano())
	cmd.Stdout = &activityWriter{w: stdoutWriter, last: &lastActivity}
	cmd.Stderr = &activityWriter{w: stderrWriter, last: &lastActivity}

	if err := cmd.Start(); err != nil {
		_ = stdoutFile.Close()
		_ = stderrFile.Close()
		return RunResult{}, &CommandError{Kind: CommandErrorStart, Err: fmt.Errorf("start %s: %w", command.Argv[0], err)}
	}

	// Track the freshly created child process group so galley daemon stop
	// --force can SIGKILL it if the daemon itself is being torn down.
	// Registration is best-effort: unregister always runs when Wait returns so
	// transient registry write errors cannot leak entries.
	registry := DefaultChildRegistry()
	if registry != nil && cmd.Process != nil {
		pgid := cmd.Process.Pid
		if reportedPGID, perr := processGroupID(cmd); perr == nil && reportedPGID > 0 {
			pgid = reportedPGID
		}
		_ = registry.Register(ChildRecord{
			PID:     cmd.Process.Pid,
			PGID:    pgid,
			Argv0:   command.Argv[0],
			WorkDir: command.WorkDir,
		})
	}
	registeredPID := 0
	if cmd.Process != nil {
		registeredPID = cmd.Process.Pid
	}

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	var idleTimedOut atomic.Bool
	watchdogStop := make(chan struct{})
	if opts.IdleTimeout > 0 {
		go func() {
			interval := opts.IdleTimeout / 4
			if interval < minIdleCheckInterval {
				interval = minIdleCheckInterval
			}
			if interval > maxIdleCheckInterval {
				interval = maxIdleCheckInterval
			}
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				select {
				case <-watchdogStop:
					return
				case now := <-ticker.C:
					if now.Sub(time.Unix(0, lastActivity.Load())) >= opts.IdleTimeout {
						idleTimedOut.Store(true)
						killProcessGroup(cmd)
						return
					}
				}
			}
		}()
	}

	var runErr error
	waitTimedOut := false
	select {
	case runErr = <-done:
	case <-runCtx.Done():
		killProcessGroup(cmd)
		select {
		case runErr = <-done:
		case <-time.After(processCancelWaitLimit):
			waitTimedOut = true
			runErr = fmt.Errorf("process did not exit after cancellation")
		}
	}
	close(watchdogStop)

	result := RunResult{
		ExitCode:     -1,
		Stdout:       stdout.String(),
		Stderr:       stderr.String(),
		Duration:     time.Since(started),
		TimedOut:     errors.Is(runCtx.Err(), context.DeadlineExceeded),
		IdleTimedOut: idleTimedOut.Load(),
	}
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}
	var closeErr error
	if !waitTimedOut {
		closeErr = errors.Join(stdoutFile.Close(), stderrFile.Close())
		if registry != nil && registeredPID > 0 {
			_ = registry.Unregister(registeredPID)
		}
	} else {
		// cmd.Wait owns the stdout/stderr copy goroutines. Closing the capture
		// files before it returns can race with those writers, so final close is
		// deferred to a goroutine. If the OS never reaps the process, this leaks
		// until the daemon exits; at that point returning is preferable to blocking
		// all task processing indefinitely.
		go func() {
			<-done
			_ = stdoutFile.Close()
			_ = stderrFile.Close()
			if registry != nil && registeredPID > 0 {
				_ = registry.Unregister(registeredPID)
			}
		}()
	}
	if result.IdleTimedOut {
		return result, errors.Join(&CommandError{
			Kind:   CommandErrorIdleTimeout,
			Result: result,
			Err:    fmt.Errorf("command produced no output for %s (idle timeout)", opts.IdleTimeout),
		}, closeErr)
	}
	if result.TimedOut {
		kind := CommandErrorTimeout
		if waitTimedOut {
			kind = CommandErrorKilled
		}
		return result, errors.Join(&CommandError{
			Kind:   kind,
			Result: result,
			Err:    fmt.Errorf("command timed out after %s", opts.Timeout),
		}, closeErr)
	}
	if runErr != nil {
		kind := CommandErrorExitNonZero
		if strings.Contains(runErr.Error(), "signal: killed") {
			kind = CommandErrorKilled
		}
		return result, errors.Join(&CommandError{
			Kind:   kind,
			Result: result,
			Err:    fmt.Errorf("run %s: %w", command.Argv[0], runErr),
		}, closeErr)
	}
	return result, closeErr
}

// activityWriter records the time of the most recent write so the idle-output
// watchdog can detect a subprocess that has stopped emitting stdout/stderr.
type activityWriter struct {
	w    io.Writer
	last *atomic.Int64
}

func (a *activityWriter) Write(p []byte) (int, error) {
	n, err := a.w.Write(p)
	if n > 0 {
		a.last.Store(time.Now().UnixNano())
	}
	return n, err
}

type nopCloser struct{}

func (nopCloser) Close() error { return nil }

func captureWriter(buffer io.Writer, path string) (io.Writer, io.Closer, error) {
	if path == "" {
		return buffer, nopCloser{}, nil
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, nil, fmt.Errorf("create capture file %s: %w", path, err)
	}
	return io.MultiWriter(buffer, file), file, nil
}

type tailBuffer struct {
	mu    sync.Mutex
	limit int
	data  []byte
}

func newTailBuffer(limit int) *tailBuffer {
	return &tailBuffer{limit: limit}
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.limit < 0 {
		b.data = append(b.data, p...)
		return len(p), nil
	}
	b.data = append(b.data, p...)
	if len(b.data) > b.limit {
		b.data = append([]byte(nil), b.data[len(b.data)-b.limit:]...)
	}
	return len(p), nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(append([]byte(nil), b.data...))
}
