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

// ProcessInfo returns process identity fields for pid. It mirrors the Unix
// implementation's shell-out approach (ps) using Windows PowerShell, which is
// present on every supported Windows install. CreationDate is formatted as a
// stable, locale-independent ISO-8601 string so ProcessStartedAt comparisons in
// Verify and interrupted-task recovery are reproducible across calls for the
// same live process.
func ProcessInfo(pid int) (ProcessInfoResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	script := fmt.Sprintf(
		`$ErrorActionPreference='Stop';`+
			`$p=Get-CimInstance Win32_Process -Filter "ProcessId=%d";`+
			`if($null -eq $p){exit 1};`+
			`[Console]::Out.Write((ConvertTo-Json -Compress ([ordered]@{`+
			`Executable=[string]$p.ExecutablePath;`+
			`Command=[string]$p.CommandLine;`+
			`StartedAt=$p.CreationDate.ToUniversalTime().ToString("o")})))`,
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
