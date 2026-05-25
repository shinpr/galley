package profilecmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/shinpr/galley/internal/profile"
)

// LookupFunc and StatFunc let tests exercise shell discovery without relying
// on the host machine's PATH or filesystem.
type LookupFunc func(string) (string, error)
type StatFunc func(string) (os.FileInfo, error)

// Resolver controls the host interactions used by ShellArgvForOS.
type Resolver struct {
	LookPath LookupFunc
	StatFile StatFunc
}

func (r Resolver) withDefaults() Resolver {
	if r.LookPath == nil {
		r.LookPath = exec.LookPath
	}
	if r.StatFile == nil {
		r.StatFile = os.Stat
	}
	return r
}

// ShellArgvForOS returns the argv for executing a profile-owned shell command,
// plus an optional cleanup function for materialized script files and the
// resolved shell kind. It is shared by required checks and environment.setup so
// both surfaces honor required_checks.shell and required_checks.shell_path.
func ShellArgvForOS(goos, command, scratchDir string, shell profile.RequiredCheckEnvironment) ([]string, func(), string, error) {
	return ShellArgvForOSWithResolver(goos, command, scratchDir, shell, Resolver{})
}

// ShellArgvForOSWithResolver is ShellArgvForOS with injectable host lookups.
func ShellArgvForOSWithResolver(goos, command, scratchDir string, shell profile.RequiredCheckEnvironment, resolver Resolver) ([]string, func(), string, error) {
	resolver = resolver.withDefaults()
	resolved, err := resolveShellForOS(goos, shell.Shell, shell.ShellPath, resolver)
	if err != nil {
		return nil, nil, "", err
	}
	if goos != "windows" {
		return shellArgv(resolved, command), nil, resolved.Kind, nil
	}
	dir := scratchDir
	cleanup := func() {}
	if dir == "" {
		tmp, err := os.MkdirTemp("", "galley-windows-check-*")
		if err != nil {
			return nil, nil, "", fmt.Errorf("create windows verification script dir: %w", err)
		}
		dir = tmp
		cleanup = func() { _ = os.RemoveAll(tmp) }
	} else if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, "", fmt.Errorf("create windows verification script dir %s: %w", dir, err)
	}
	ext := ".cmd"
	body := []byte("@echo off\r\n" + command + "\r\n")
	if resolved.Kind == "bash" {
		ext = ".sh"
		body = []byte("#!/usr/bin/env bash\nset -e\n" + command + "\n")
	} else if resolved.Kind == "sh" {
		ext = ".sh"
		body = []byte("set -e\n" + command + "\n")
	} else if resolved.Kind == "powershell" || resolved.Kind == "pwsh" {
		ext = ".ps1"
		body = []byte("$ErrorActionPreference = 'Stop'\r\n" + command + "\r\n")
	}
	scriptPath := filepath.Join(dir, "galley-verification"+ext)
	if err := os.WriteFile(scriptPath, body, 0o600); err != nil {
		cleanup()
		return nil, nil, "", fmt.Errorf("write windows verification script %s: %w", scriptPath, err)
	}
	return shellScriptArgv(resolved, scriptPath), cleanup, resolved.Kind, nil
}

type resolvedShell struct {
	Kind string
	Bin  string
}

func resolveShellForOS(goos, configured, configuredPath string, resolver Resolver) (resolvedShell, error) {
	if configuredPath != "" {
		kind := profile.InferRequiredCheckShellKind(configuredPath)
		if kind == "" {
			switch configured {
			case "sh", "bash", "cmd", "powershell", "pwsh":
				kind = configured
			default:
				return resolvedShell{}, fmt.Errorf("required_checks.shell_path basename is not a recognized shell executable; set an explicit required_checks.shell kind (sh, bash, cmd, powershell, or pwsh) as fallback metadata")
			}
		}
		return resolvedShell{Kind: kind, Bin: configuredPath}, nil
	}
	switch configured {
	case "", "auto":
		if goos == "windows" {
			if bash, ok := discoverWindowsBash(resolver); ok {
				return resolvedShell{Kind: "bash", Bin: bash}, nil
			}
			return resolvedShell{Kind: "cmd", Bin: "cmd.exe"}, nil
		}
		return resolvedShell{Kind: "sh", Bin: "/bin/sh"}, nil
	case "sh", "bash", "cmd", "powershell", "pwsh":
		shell := shellForKind(configured)
		if goos == "windows" && configured == "bash" {
			bash, ok := discoverWindowsBash(resolver)
			if !ok {
				return resolvedShell{}, fmt.Errorf(
					"required_checks.shell is \"bash\" on Windows but no standard Git for Windows Bash install was discoverable; " +
						"PATH-discovered bash entries (WSL launcher at C:\\Windows\\System32\\bash.exe, WindowsApps shims, MSYS2, Cygwin, Scoop, or Chocolatey-managed Bashes) are not auto-selected to avoid silently switching the required-check shell; " +
						"set required_checks.shell_path to the exact bash executable path you want Galley to launch")
			}
			shell.Bin = bash
		}
		return shell, nil
	default:
		return resolvedShell{}, fmt.Errorf("unsupported required check shell %q", configured)
	}
}

// standardWindowsGitBashPaths lists the canonical Git for Windows install
// layouts that Galley should prefer for profile command shell auto-discovery.
var standardWindowsGitBashPaths = []string{
	`C:\Program Files\Git\bin\bash.exe`,
	`C:\Program Files\Git\usr\bin\bash.exe`,
	`C:\Program Files (x86)\Git\bin\bash.exe`,
	`C:\Program Files (x86)\Git\usr\bin\bash.exe`,
}

func discoverWindowsBash(resolver Resolver) (string, bool) {
	for _, candidate := range standardWindowsGitBashPaths {
		if _, err := resolver.StatFile(candidate); err == nil {
			return candidate, true
		}
	}
	for _, name := range []string{"git.exe", "git"} {
		gitPath, err := resolver.LookPath(name)
		if err != nil {
			continue
		}
		if bashPath := gitBashFromGitPath(gitPath); bashPath != "" {
			if _, err := resolver.StatFile(bashPath); err == nil {
				return bashPath, true
			}
		}
	}
	for _, name := range []string{"bash.exe", "bash"} {
		path, err := resolver.LookPath(name)
		if err != nil {
			continue
		}
		if !isStandardGitForWindowsBashPath(path) {
			continue
		}
		return path, true
	}
	return "", false
}

func isStandardGitForWindowsBashPath(path string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(path, `\`, `/`))
	for _, std := range standardWindowsGitBashPaths {
		if normalized == strings.ToLower(strings.ReplaceAll(std, `\`, `/`)) {
			return true
		}
	}
	return false
}

func gitBashFromGitPath(gitPath string) string {
	normalized := strings.ReplaceAll(gitPath, `\`, `/`)
	lower := strings.ToLower(normalized)
	for _, suffix := range []string{"/cmd/git.exe", "/cmd/git"} {
		if strings.HasSuffix(lower, suffix) {
			return normalized[:len(normalized)-len(suffix)] + "/bin/bash.exe"
		}
	}
	return ""
}

func shellForKind(kind string) resolvedShell {
	switch kind {
	case "bash":
		return resolvedShell{Kind: kind, Bin: "bash"}
	case "cmd":
		return resolvedShell{Kind: kind, Bin: "cmd.exe"}
	case "powershell":
		return resolvedShell{Kind: kind, Bin: "powershell.exe"}
	case "pwsh":
		return resolvedShell{Kind: kind, Bin: "pwsh"}
	default:
		return resolvedShell{Kind: "sh", Bin: "/bin/sh"}
	}
}

func shellArgv(shell resolvedShell, command string) []string {
	switch shell.Kind {
	case "bash":
		return []string{shell.Bin, "-c", command}
	case "cmd":
		return []string{shell.Bin, "/C", command}
	case "powershell":
		return []string{shell.Bin, "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", command}
	case "pwsh":
		return []string{shell.Bin, "-NoProfile", "-Command", command}
	default:
		return []string{shell.Bin, "-c", command}
	}
}

func shellScriptArgv(shell resolvedShell, scriptPath string) []string {
	switch shell.Kind {
	case "bash":
		return []string{shell.Bin, scriptPath}
	case "sh":
		return []string{shell.Bin, scriptPath}
	case "powershell":
		return []string{shell.Bin, "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", scriptPath}
	case "pwsh":
		return []string{shell.Bin, "-NoProfile", "-File", scriptPath}
	default:
		return []string{shell.Bin, "/C", scriptPath}
	}
}
