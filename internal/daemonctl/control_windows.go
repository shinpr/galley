//go:build windows

package daemonctl

import (
	"errors"
	"fmt"
	"math"
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

// windowsProcessID narrows a PID for the Windows process APIs, which take an
// unsigned 32-bit id.
func windowsProcessID(pid int) (uint32, error) {
	if pid <= 0 || int64(pid) > int64(math.MaxUint32) {
		return 0, fmt.Errorf("pid %d is outside the Windows process id range", pid)
	}
	return uint32(pid), nil
}

// Alive reports whether pid exists by opening the process and inspecting its
// exit code. Windows has no signal(0) equivalent: `process.Signal(Signal(0))`
// surfaces a raw "not supported by windows" error from the Go runtime.
func Alive(pid int) (bool, error) {
	id, err := windowsProcessID(pid)
	if err != nil {
		return false, nil
	}
	handle, err := syscall.OpenProcess(processQueryLimitedInformation, false, id)
	if err != nil {
		return aliveFromOpenError(err), nil
	}
	defer func() { _ = syscall.CloseHandle(handle) }()
	var code uint32
	if err := syscall.GetExitCodeProcess(handle, &code); err != nil {
		return false, fmt.Errorf("read exit code of process %d: %w", pid, err)
	}
	return code == stillActive, nil
}

// aliveFromOpenError classifies an OpenProcess failure. ERROR_INVALID_PARAMETER
// means the pid does not exist; ERROR_ACCESS_DENIED still implies a live
// process owned by another user. Any other error reports not alive so daemon
// status shows a clean stopped state instead of a raw Windows API error.
func aliveFromOpenError(err error) bool {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return false
	}
	return errno == syscall.Errno(5) /* ERROR_ACCESS_DENIED */
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
		return fmt.Errorf("find process %d: %w", pid, err)
	}
	if err := process.Kill(); err != nil {
		if errors.Is(err, os.ErrProcessDone) {
			return ErrNotRunning
		}
		return fmt.Errorf("terminate process %d: %w", pid, err)
	}
	return waitExit(pid, timeout, "stop")
}
