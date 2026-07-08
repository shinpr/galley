//go:build !darwin && !linux && !windows

package daemonctl

import "fmt"

// ProcessInfoResult contains process identity fields from the OS process table.
type ProcessInfoResult struct {
	Executable string
	Command    string
	StartedAt  string
}

// ProcessInfo returns process identity fields for pid.
func ProcessInfo(pid int) (ProcessInfoResult, error) {
	return ProcessInfoResult{}, fmt.Errorf("process command inspection is not implemented on this platform")
}

// Zombie reports whether pid is a zombie process.
func Zombie(pid int) bool {
	return false
}
