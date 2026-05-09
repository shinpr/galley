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
	"time"
)

const (
	defaultTailBytes       = 64 * 1024
	processCancelWaitLimit = 5 * time.Second
)

// RunOptions controls subprocess execution and optional audit file capture.
type RunOptions struct {
	Timeout    time.Duration
	StdoutPath string
	StderrPath string
	// TailBytes controls in-memory stdout/stderr tail size. Zero uses the default; negative keeps all output.
	TailBytes int
}

// RunResult reports the observable result of a subprocess run.
type RunResult struct {
	ExitCode int           `json:"exit_code"`
	Stdout   string        `json:"stdout,omitempty"`
	Stderr   string        `json:"stderr,omitempty"`
	Duration time.Duration `json:"duration"`
	TimedOut bool          `json:"timed_out"`
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
	if command.Env != nil {
		cmd.Env = command.Env
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

	cmd.Stdout = stdoutWriter
	cmd.Stderr = stderrWriter

	if err := cmd.Start(); err != nil {
		_ = stdoutFile.Close()
		_ = stderrFile.Close()
		return RunResult{}, fmt.Errorf("start %s: %w", command.Argv[0], err)
	}

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

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

	result := RunResult{
		ExitCode: -1,
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		Duration: time.Since(started),
		TimedOut: errors.Is(runCtx.Err(), context.DeadlineExceeded),
	}
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}
	var closeErr error
	if !waitTimedOut {
		closeErr = errors.Join(stdoutFile.Close(), stderrFile.Close())
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
		}()
	}
	if result.TimedOut {
		return result, errors.Join(fmt.Errorf("command timed out after %s", opts.Timeout), closeErr)
	}
	if runErr != nil {
		return result, errors.Join(fmt.Errorf("run %s: %w", command.Argv[0], runErr), closeErr)
	}
	return result, closeErr
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
