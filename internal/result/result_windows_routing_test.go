package result

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This test file compiles on every OS and passes an explicit goos value into
// shellArgvForOS.

// TestShellArgvForOSWindowsUsesScriptFile pins AC5 for required quality-profile
// checks: Windows verification commands use a single .cmd script execution
// shape regardless of command length, so the command body never reaches argv
// and short/long commands do not diverge into different cmd.exe contexts.
func TestShellArgvForOSWindowsUsesScriptFile(t *testing.T) {
	scratch := t.TempDir()
	command := strings.Repeat("echo placeholder && ", 400) + "echo done"

	argv, cleanup, shell, err := shellArgvForOS("windows", command, scratch, "cmd", "")
	if err != nil {
		t.Fatal(err)
	}
	if cleanup != nil {
		defer cleanup()
	}

	if len(argv) != 3 || argv[0] != "cmd.exe" || argv[1] != "/C" {
		t.Fatalf("Windows shellArgv must invoke cmd.exe /C <path>, got %#v", argv)
	}
	if shell != "cmd" {
		t.Fatalf("resolved shell got %q, want cmd", shell)
	}
	if strings.Contains(argv[2], "echo placeholder") {
		t.Fatalf("Windows long-command argv must reference a script path, not the command body: %q", argv[2])
	}
	if !strings.HasSuffix(argv[2], ".cmd") {
		t.Fatalf("Windows long-command argv must reference a .cmd script, got %q", argv[2])
	}

	body, err := os.ReadFile(argv[2])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "echo done") {
		t.Fatal("Windows .cmd script must contain the verification command body")
	}
	if !strings.HasPrefix(string(body), "@echo off") {
		t.Fatalf("Windows .cmd script should suppress command echoing, got %q", body[:32])
	}

	// AC1: total argv byte length must stay well below Windows safe limit.
	total := 0
	for _, a := range argv {
		total += len(a) + 1
	}
	if total > 4096 {
		t.Fatalf("Windows long-command argv length %d exceeds safe threshold", total)
	}
}

// TestShellArgvForOSWindowsShortCommandAlsoUsesScriptFile ensures the Windows
// execution shape is independent of command length.
func TestShellArgvForOSWindowsShortCommandAlsoUsesScriptFile(t *testing.T) {
	scratch := t.TempDir()
	argv, cleanup, shell, err := shellArgvForOS("windows", "go test ./...", scratch, "cmd", "")
	if err != nil {
		t.Fatal(err)
	}
	if cleanup != nil {
		defer cleanup()
	}
	if len(argv) != 3 || argv[0] != "cmd.exe" || argv[1] != "/C" {
		t.Fatalf("short Windows shellArgv must invoke cmd.exe /C <path>, got %#v", argv)
	}
	if shell != "cmd" {
		t.Fatalf("resolved shell got %q, want cmd", shell)
	}
	if argv[2] == "go test ./..." || !strings.HasSuffix(argv[2], ".cmd") {
		t.Fatalf("short Windows shellArgv must reference a .cmd script path, got %#v", argv)
	}
	body, err := os.ReadFile(argv[2])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "go test ./...") {
		t.Fatalf("short Windows .cmd script must contain the command body, got %q", body)
	}
}

func TestShellArgvForOSWindowsCreatesScratchDir(t *testing.T) {
	scratch := t.TempDir() + "/nested/checks"
	argv, cleanup, _, err := shellArgvForOS("windows", "go test ./...", scratch, "cmd", "")
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
	argv, cleanup, shell, err := shellArgvForOS("linux", long, "", "", "")
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
	argv, cleanup, shell, err := shellArgvForOS("windows", "grep -F ok proof.txt", scratch, "", "")
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

func TestShellArgvForOSWindowsAutoFallsBackToCmdWhenBashMissing(t *testing.T) {
	old := lookPath
	oldStat := statFile
	lookPath = func(file string) (string, error) {
		return "", os.ErrNotExist
	}
	statFile = func(name string) (os.FileInfo, error) {
		return nil, os.ErrNotExist
	}
	defer func() {
		lookPath = old
		statFile = oldStat
	}()

	argv, cleanup, shell, err := shellArgvForOS("windows", "go test ./...", t.TempDir(), "", "")
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
		t.Fatalf("Windows auto without bash must invoke cmd.exe /C <script>, got %#v", argv)
	}
}

func TestShellArgvForOSWindowsAutoFindsGitBashFromGitPath(t *testing.T) {
	old := lookPath
	oldStat := statFile
	lookPath = func(file string) (string, error) {
		switch file {
		case "git.exe":
			return `C:\Program Files\Git\cmd\git.exe`, nil
		default:
			return "", os.ErrNotExist
		}
	}
	statFile = func(name string) (os.FileInfo, error) {
		if name == "C:/Program Files/Git/bin/bash.exe" {
			return fakeFileInfo{}, nil
		}
		return nil, os.ErrNotExist
	}
	defer func() {
		lookPath = old
		statFile = oldStat
	}()

	argv, cleanup, shell, err := shellArgvForOS("windows", "grep -F ok proof.txt", t.TempDir(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	if cleanup != nil {
		defer cleanup()
	}
	if shell != "bash" {
		t.Fatalf("resolved shell got %q, want bash", shell)
	}
	if len(argv) != 2 || argv[0] != "C:/Program Files/Git/bin/bash.exe" || !strings.HasSuffix(argv[1], ".sh") {
		t.Fatalf("Windows auto should infer Git Bash from git.exe path, got %#v", argv)
	}
}

func TestShellArgvForOSWindowsExplicitPwshUsesPowerShellScript(t *testing.T) {
	argv, cleanup, shell, err := shellArgvForOS("windows", "Write-Output ok", t.TempDir(), "pwsh", "")
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
