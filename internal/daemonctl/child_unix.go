//go:build !windows

package daemonctl

import (
	"os"
	"syscall"
)

// TerminateChildProcess requests termination of a child process spawned by
// `galley daemon start` when its post-start bring-up fails (WritePID or
// readiness probe). On Unix the conventional primitive is SIGTERM, which
// the foreground daemon's signal handler translates into graceful shutdown.
// The helper exists so the caller does not import `syscall` directly and so
// the Windows build can supply a TerminateProcess-based implementation that
// does not surface "signal not supported by windows" when start cleanup
// runs on a console-less background daemon.
func TerminateChildProcess(p *os.Process) error {
	if p == nil {
		return nil
	}
	return p.Signal(syscall.SIGTERM)
}
