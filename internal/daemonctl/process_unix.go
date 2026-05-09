//go:build darwin || linux

package daemonctl

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// ProcessInfoResult contains process identity fields from the OS process table.
type ProcessInfoResult struct {
	Executable string
	Command    string
	StartedAt  string
}

// ProcessInfo returns process identity fields for pid.
func ProcessInfo(pid int) (ProcessInfoResult, error) {
	pidText := strconv.Itoa(pid)
	executable, err := psField(pidText, "comm=")
	if err != nil {
		return ProcessInfoResult{}, err
	}
	command, err := psField(pidText, "command=")
	if err != nil {
		return ProcessInfoResult{}, err
	}
	startedAt, err := psField(pidText, "lstart=")
	if err != nil {
		return ProcessInfoResult{}, err
	}
	return ProcessInfoResult{
		Executable: executable,
		Command:    command,
		StartedAt:  startedAt,
	}, nil
}

func psField(pid, field string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "ps", "-p", pid, "-o", field).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

// Zombie reports whether pid is a zombie process.
func Zombie(pid int) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "ps", "-p", strconv.Itoa(pid), "-o", "stat=").Output()
	if err != nil {
		return false
	}
	return strings.HasPrefix(strings.TrimSpace(string(output)), "Z")
}
