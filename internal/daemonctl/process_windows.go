//go:build windows

package daemonctl

import (
	"fmt"
	"time"

	"golang.org/x/sys/windows"
)

// ProcessInfoResult contains process identity fields from the OS process table.
type ProcessInfoResult struct {
	Executable string
	Command    string
	StartedAt  string
}

// ProcessInfo returns stable identity fields directly from the Windows
// process API without starting an external query process.
func ProcessInfo(pid int) (ProcessInfoResult, error) {
	id, err := windowsProcessID(pid)
	if err != nil {
		return ProcessInfoResult{}, fmt.Errorf("query process: %w", err)
	}
	handle, err := windows.OpenProcess(processQueryLimitedInformation, false, id)
	if err != nil {
		return ProcessInfoResult{}, fmt.Errorf("query process %d: open: %w", pid, err)
	}
	defer func() { _ = windows.CloseHandle(handle) }()

	// The buffer length is typed so the Windows API call needs no conversion.
	const pathBufferLen uint32 = 32768
	pathBuffer := make([]uint16, pathBufferLen)
	pathLength := pathBufferLen
	if err := windows.QueryFullProcessImageName(handle, 0, &pathBuffer[0], &pathLength); err != nil {
		return ProcessInfoResult{}, fmt.Errorf("query process %d executable: %w", pid, err)
	}

	var creationTime, exitTime, kernelTime, userTime windows.Filetime
	if err := windows.GetProcessTimes(handle, &creationTime, &exitTime, &kernelTime, &userTime); err != nil {
		return ProcessInfoResult{}, fmt.Errorf("query process %d start time: %w", pid, err)
	}

	return ProcessInfoResult{
		Executable: windows.UTF16ToString(pathBuffer[:pathLength]),
		StartedAt:  time.Unix(0, creationTime.Nanoseconds()).UTC().Format(time.RFC3339Nano),
	}, nil
}

// Zombie reports whether pid is a zombie process. Windows has no zombie
// processes, so this is always false.
func Zombie(_ int) bool {
	return false
}
