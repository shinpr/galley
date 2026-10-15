package profilecmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shinpr/galley/internal/profile"
)

// This test file compiles on every OS and passes an explicit goos value into
// shellArgvForOS.

var (
	lookPath = exec.LookPath
	statFile = os.Stat
)

type shellArgvSpec struct {
	GOOS                string
	Command             string
	ScratchDir          string
	ConfiguredShell     string
	ConfiguredShellPath string
}

func shellArgvForOS(spec shellArgvSpec) ([]string, func(), string, error) {
	goos, command, scratchDir := spec.GOOS, spec.Command, spec.ScratchDir
	configuredShell, configuredShellPath := spec.ConfiguredShell, spec.ConfiguredShellPath
	return ShellArgvForOSWithResolver(ShellRequest{GOOS: goos, Command: command, ScratchDir: scratchDir, Shell: profile.RequiredCheckEnvironment{
		Shell:     configuredShell,
		ShellPath: configuredShellPath,
	}}, Resolver{
		LookPath: lookPath,
		StatFile: statFile,
	})
}

// TestShellArgvForOSWindowsCmdPreservesMultilineVerbatim verifies a multi-line
// cmd required check is written into the .cmd script exactly as authored, so
// control-flow constructs (if/for blocks, `^` continuations) are not mangled.
func TestShellArgvForOSWindowsCmdPreservesMultilineVerbatim(t *testing.T) {
	scratch := t.TempDir()
	command := "if exist build.bat (\r\n  build.bat\r\n) else (\r\n  echo skip\r\n)"

	argv, cleanup, _, err := shellArgvForOS(shellArgvSpec{GOOS: "windows", Command: command, ScratchDir: scratch, ConfiguredShell: "cmd", ConfiguredShellPath: ""})
	if err != nil {
		t.Fatal(err)
	}
	if cleanup != nil {
		defer cleanup()
	}
	body, err := os.ReadFile(argv[2])
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	if !strings.Contains(got, command) {
		t.Fatalf("multi-line command must be preserved verbatim, got:\n%s", got)
	}
	if strings.Contains(got, "if errorlevel 1 exit /b 1") {
		t.Fatalf("no per-line guard must be injected into the cmd script, got:\n%s", got)
	}
}

// Windows commands always run from a generated .cmd script, so the command body
// never reaches argv and short and long commands share one cmd.exe context.
func TestShellArgvForOSWindowsUsesScriptFileRegardlessOfLength(t *testing.T) {
	for name, command := range map[string]string{
		"short": "go test ./...",
		"long":  strings.Repeat("echo placeholder && ", 400) + "echo done",
	} {
		t.Run(name, func(t *testing.T) {
			argv, cleanup, shell, err := shellArgvForOS(shellArgvSpec{GOOS: "windows", Command: command, ScratchDir: t.TempDir(), ConfiguredShell: "cmd"})
			if err != nil {
				t.Fatal(err)
			}
			if cleanup != nil {
				defer cleanup()
			}
			if shell != "cmd" {
				t.Fatalf("resolved shell got %q, want cmd", shell)
			}
			if len(argv) != 3 || argv[0] != "cmd.exe" || argv[1] != "/C" || !strings.HasSuffix(argv[2], ".cmd") {
				t.Fatalf("Windows shellArgv must invoke cmd.exe /C <script>.cmd, got %#v", argv)
			}
			if strings.Contains(argv[2], command) {
				t.Fatalf("Windows argv must reference a script path, not the command body: %q", argv[2])
			}
			body, err := os.ReadFile(argv[2])
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(string(body), "@echo off") || !strings.Contains(string(body), command) {
				t.Fatalf("Windows .cmd script must suppress echo and contain the command body, got %q", body)
			}
		})
	}
}

func TestShellArgvForOSWindowsCreatesScratchDir(t *testing.T) {
	scratch := t.TempDir() + "/nested/checks"
	argv, cleanup, _, err := shellArgvForOS(shellArgvSpec{GOOS: "windows", Command: "go test ./...", ScratchDir: scratch, ConfiguredShell: "cmd", ConfiguredShellPath: ""})
	if err != nil {
		t.Fatal(err)
	}
	if cleanup != nil {
		defer cleanup()
	}
	if _, err := os.Stat(argv[2]); err != nil {
		t.Fatalf("Windows .cmd script should be written under caller scratch dir: %v", err)
	}
	if filepath.Clean(filepath.Dir(argv[2])) != filepath.Clean(scratch) {
		t.Fatalf("Windows .cmd script got %q, want under %q", argv[2], scratch)
	}
}

// TestShellArgvForOSNonWindowsKeepsShape pins AC4: macOS/Linux continue to
// use /bin/sh -c and the existing argv shape regardless of command length.
func TestShellArgvForOSNonWindowsKeepsShape(t *testing.T) {
	long := strings.Repeat("echo placeholder && ", 1000) + "echo done"
	argv, cleanup, shell, err := shellArgvForOS(shellArgvSpec{GOOS: "linux", Command: long, ScratchDir: "", ConfiguredShell: "", ConfiguredShellPath: ""})
	if err != nil {
		t.Fatal(err)
	}
	if cleanup != nil {
		t.Fatal("non-Windows commands must not produce a cleanup function")
	}
	if len(argv) != 3 || argv[0] != "/bin/sh" || argv[1] != "-c" || argv[2] != long {
		t.Fatalf("non-Windows shellArgv must keep /bin/sh -c shape, got %#v[0:3]", argv)
	}
	if shell != "sh" {
		t.Fatalf("resolved shell got %q, want sh", shell)
	}
}

func TestShellArgvForOSWindowsAutoPrefersBashWhenPresent(t *testing.T) {
	old := lookPath
	oldStat := statFile
	lookPath = func(file string) (string, error) {
		if file == "bash.exe" {
			return `C:\Program Files\Git\bin\bash.exe`, nil
		}
		return "", os.ErrNotExist
	}
	defer func() {
		lookPath = old
		statFile = oldStat
	}()

	scratch := t.TempDir()
	argv, cleanup, shell, err := shellArgvForOS(shellArgvSpec{GOOS: "windows", Command: "grep -F ok proof.txt", ScratchDir: scratch, ConfiguredShell: "", ConfiguredShellPath: ""})
	if err != nil {
		t.Fatal(err)
	}
	if cleanup != nil {
		defer cleanup()
	}
	if shell != "bash" {
		t.Fatalf("resolved shell got %q, want bash", shell)
	}
	if len(argv) != 2 || argv[0] != `C:\Program Files\Git\bin\bash.exe` || !strings.HasSuffix(argv[1], ".sh") {
		t.Fatalf("Windows auto with bash must invoke bash <script>, got %#v", argv)
	}
	body, err := os.ReadFile(argv[1])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "grep -F ok proof.txt") {
		t.Fatalf("bash script must contain command body, got %q", body)
	}
}

func TestShellArgvForOSWindowsExplicitPwshUsesPowerShellScript(t *testing.T) {
	argv, cleanup, shell, err := shellArgvForOS(shellArgvSpec{GOOS: "windows", Command: "Write-Output ok", ScratchDir: t.TempDir(), ConfiguredShell: "pwsh", ConfiguredShellPath: ""})
	if err != nil {
		t.Fatal(err)
	}
	if cleanup != nil {
		defer cleanup()
	}
	if shell != "pwsh" {
		t.Fatalf("resolved shell got %q, want pwsh", shell)
	}
	if len(argv) != 4 || argv[0] != "pwsh" || argv[1] != "-NoProfile" || argv[2] != "-File" || !strings.HasSuffix(argv[3], ".ps1") {
		t.Fatalf("Windows explicit pwsh must invoke pwsh -NoProfile -File <script>, got %#v", argv)
	}
}

type fakeFileInfo struct{}

func (fakeFileInfo) Name() string       { return "bash.exe" }
func (fakeFileInfo) Size() int64        { return 0 }
func (fakeFileInfo) Mode() os.FileMode  { return 0o755 }
func (fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (fakeFileInfo) IsDir() bool        { return false }
func (fakeFileInfo) Sys() any           { return nil }
