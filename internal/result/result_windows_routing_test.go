package result

import (
	"os"
	"strings"
	"testing"
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

	argv, cleanup, err := shellArgvForOS("windows", command, scratch)
	if err != nil {
		t.Fatal(err)
	}
	if cleanup != nil {
		defer cleanup()
	}

	if len(argv) != 3 || argv[0] != "cmd.exe" || argv[1] != "/C" {
		t.Fatalf("Windows shellArgv must invoke cmd.exe /C <path>, got %#v", argv)
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
	argv, cleanup, err := shellArgvForOS("windows", "go test ./...", scratch)
	if err != nil {
		t.Fatal(err)
	}
	if cleanup != nil {
		defer cleanup()
	}
	if len(argv) != 3 || argv[0] != "cmd.exe" || argv[1] != "/C" {
		t.Fatalf("short Windows shellArgv must invoke cmd.exe /C <path>, got %#v", argv)
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
	argv, cleanup, err := shellArgvForOS("windows", "go test ./...", scratch)
	if err != nil {
		t.Fatal(err)
	}
	if cleanup != nil {
		defer cleanup()
	}
	if _, err := os.Stat(argv[2]); err != nil {
		t.Fatalf("Windows .cmd script should be written under caller scratch dir: %v", err)
	}
	if !strings.HasPrefix(argv[2], scratch) {
		t.Fatalf("Windows .cmd script got %q, want under %q", argv[2], scratch)
	}
}

// TestShellArgvForOSNonWindowsKeepsShape pins AC4: macOS/Linux continue to
// use /bin/sh -c and the existing argv shape regardless of command length.
func TestShellArgvForOSNonWindowsKeepsShape(t *testing.T) {
	long := strings.Repeat("echo placeholder && ", 1000) + "echo done"
	argv, cleanup, err := shellArgvForOS("linux", long, "")
	if err != nil {
		t.Fatal(err)
	}
	if cleanup != nil {
		t.Fatal("non-Windows commands must not produce a cleanup function")
	}
	if len(argv) != 3 || argv[0] != "/bin/sh" || argv[1] != "-c" || argv[2] != long {
		t.Fatalf("non-Windows shellArgv must keep /bin/sh -c shape, got %#v[0:3]", argv)
	}
}
