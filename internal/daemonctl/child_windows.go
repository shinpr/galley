//go:build windows

package daemonctl

import (
	"errors"
	"os"
)

// TerminateChildProcess requests termination of a child process spawned by
// `galley daemon start` when its post-start bring-up fails (WritePID or
// readiness probe). Windows has no SIGTERM equivalent that can be delivered
// to a console-less background process, so `process.Signal(syscall.SIGTERM)`
// returns "not supported by windows" and leaves the child running after
// start cleanup. The Windows path therefore uses `Process.Kill`, which maps
// to TerminateProcess and matches the immediate-termination semantics used
// by `daemon stop` on this OS. An already-exited child returns
// `os.ErrProcessDone`, which the helper treats as success because the
// caller's intent (no orphaned process) is satisfied.
func TerminateChildProcess(p *os.Process) error {
	if p == nil {
		return nil
	}
	if err := p.Kill(); err != nil {
		if errors.Is(err, os.ErrProcessDone) {
			return nil
		}
		return err
	}
	return nil
}
