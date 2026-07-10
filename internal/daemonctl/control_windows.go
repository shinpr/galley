//go:build windows

package daemonctl

import (
	"errors"
	"os"
	"syscall"
	"time"
)

// stillActive matches Windows STILL_ACTIVE (259); a process with this exit
// code from GetExitCodeProcess is still running.
const stillActive = 259

// processQueryLimitedInformation is the Windows constant
// PROCESS_QUERY_LIMITED_INFORMATION. The stdlib `syscall` package on Windows
// does not export this constant by name, so we declare it locally to avoid
// pulling in `golang.org/x/sys/windows` for one numeric value.
const processQueryLimitedInformation = 0x1000

// Alive reports whether pid exists by opening the process and inspecting its
// exit code. Windows has no signal(0) equivalent: `process.Signal(Signal(0))`
// surfaces a raw "not supported by windows" error from the Go runtime.
func Alive(pid int) (bool, error) {
	if pid <= 0 {
		return false, nil
	}
	handle, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		// ERROR_INVALID_PARAMETER (87) means the pid does not exist; the
		// other documented failure mode here is access-denied, which still
		// implies a live process owned by another user. Treat unknown errors
		// as "not alive" so daemon status reports a clean stopped state
		// rather than surfacing a Windows API error to the operator.
		var errno syscall.Errno
		if errors.As(err, &errno) {
			if errno == syscall.Errno(5) /* ERROR_ACCESS_DENIED */ {
				return true, nil
			}
			if errno == syscall.Errno(87) /* ERROR_INVALID_PARAMETER */ {
				return false, nil
			}
		}
		return false, nil
	}
	defer syscall.CloseHandle(handle)
	var code uint32
	if err := syscall.GetExitCodeProcess(handle, &code); err != nil {
		return false, err
	}
	return code == stillActive, nil
}

// Stop terminates pid. Windows does not have a SIGTERM equivalent that can
// be delivered to a console-less background process, so background daemon
// stop performs an immediate TerminateProcess (the same primitive used by
// `Kill`). Operators that need a graceful shutdown should run the daemon in
// the foreground via `galley daemon run` and use Ctrl+C instead of
// `galley daemon start`/`stop`. This limitation is documented in
// CHANGELOG.md and docs/operations.md.
func Stop(pid int, timeout time.Duration) error {
	alive, err := Alive(pid)
	if err != nil {
		return err
	}
	if !alive {
		return ErrNotRunning
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	if err := process.Kill(); err != nil {
		if errors.Is(err, os.ErrProcessDone) {
			return ErrNotRunning
		}
		return err
	}
	return waitExit(pid, timeout, "stop")
}
