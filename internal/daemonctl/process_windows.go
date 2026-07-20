//go:build windows

package daemonctl

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// ProcessInfoResult contains process identity fields from the OS process table.
type ProcessInfoResult struct {
	Executable string
	Command    string
	StartedAt  string
}

// ProcessInfo returns stable identity fields for a Windows process using
// PowerShell's direct process API rather than the transient CIM service.
func ProcessInfo(pid int) (ProcessInfoResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	script := fmt.Sprintf(
		`$ErrorActionPreference='Stop';`+
			`$p=Get-Process -Id %d -ErrorAction Stop;`+
			`[Console]::Out.Write((ConvertTo-Json -Compress ([ordered]@{`+
			`Executable=[string]$p.Path;`+
			`Command='';`+
			`StartedAt=$p.StartTime.ToUniversalTime().ToString("o")})))`,
		pid)
	output, err := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", script).Output()
	if err != nil {
		return ProcessInfoResult{}, fmt.Errorf("query process %d: %w", pid, err)
	}
	var parsed ProcessInfoResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(output))), &parsed); err != nil {
		return ProcessInfoResult{}, fmt.Errorf("parse process %d info: %w", pid, err)
	}
	return parsed, nil
}

// Zombie reports whether pid is a zombie process. Windows has no zombie
// processes, so this is always false.
func Zombie(pid int) bool {
	return false
}
