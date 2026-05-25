package profilecmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shinpr/galley/internal/profile"
)

func TestShellArgvForOSHonorsExplicitShellPathWithoutDiscovery(t *testing.T) {
	lookCalls := 0
	statCalls := 0
	argv, cleanup, shell, err := ShellArgvForOSWithResolver("linux", "echo ok", "", profile.RequiredCheckEnvironment{
		ShellPath: "/opt/galley/bin/bash",
	}, Resolver{
		LookPath: func(string) (string, error) {
			lookCalls++
			return "", os.ErrNotExist
		},
		StatFile: func(string) (os.FileInfo, error) {
			statCalls++
			return nil, os.ErrNotExist
		},
	})
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		t.Fatalf("shell argv: %v", err)
	}
	if shell != "bash" {
		t.Fatalf("shell got %q, want bash", shell)
	}
	if got := argv[0]; got != "/opt/galley/bin/bash" {
		t.Fatalf("argv[0] got %q, want explicit shell_path", got)
	}
	if lookCalls != 0 || statCalls != 0 {
		t.Fatalf("explicit shell_path must skip discovery, look=%d stat=%d", lookCalls, statCalls)
	}
}

func TestShellArgvForOSWindowsBashRejectsNonStandardPathDiscovery(t *testing.T) {
	_, cleanup, _, err := ShellArgvForOSWithResolver("windows", "echo ok", t.TempDir(), profile.RequiredCheckEnvironment{
		Shell: "bash",
	}, Resolver{
		LookPath: func(file string) (string, error) {
			if file == "bash.exe" || file == "bash" {
				return `C:\Windows\System32\bash.exe`, nil
			}
			return "", os.ErrNotExist
		},
		StatFile: func(string) (os.FileInfo, error) {
			return nil, os.ErrNotExist
		},
	})
	if cleanup != nil {
		defer cleanup()
	}
	if err == nil {
		t.Fatal("expected non-standard Windows bash discovery to fail")
	}
	if !strings.Contains(err.Error(), "required_checks.shell_path") {
		t.Fatalf("error must name shell_path override, got %q", err.Error())
	}
}

func TestShellArgvForOSWindowsPowershellUsesProfileShellShape(t *testing.T) {
	scratch := t.TempDir()
	argv, cleanup, shell, err := ShellArgvForOS("windows", "Write-Output ok", scratch, profile.RequiredCheckEnvironment{
		Shell: "pwsh",
	})
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		t.Fatalf("shell argv: %v", err)
	}
	if shell != "pwsh" {
		t.Fatalf("shell got %q, want pwsh", shell)
	}
	if len(argv) < 4 || argv[0] != "pwsh" || argv[1] != "-NoProfile" || argv[len(argv)-2] != "-File" {
		t.Fatalf("pwsh argv shape got %#v", argv)
	}
	scriptPath := argv[len(argv)-1]
	if filepath.Ext(scriptPath) != ".ps1" {
		t.Fatalf("pwsh script path got %q, want .ps1", scriptPath)
	}
}
