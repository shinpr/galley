package result

import (
	"os"
	"strings"
	"testing"
)

// TestShellArgvForOSWindowsLongCommandUsesScriptFile pins AC5 for required
// quality-profile checks: when a Windows verification command is long enough
// to risk the 8191-character cmd.exe limit, Galley materializes the command
// body into a .cmd script and invokes cmd.exe with the script path so the
// command body never reaches argv.
func TestShellArgvForOSWindowsLongCommandUsesScriptFile(t *testing.T) {
	scratch := t.TempDir()
	long := strings.Repeat("echo placeholder && ", windowsShellArgvLengthThreshold/20+10) + "echo done"
	if len(long) < windowsShellArgvLengthThreshold {
		t.Fatalf("test fixture is too short: %d", len(long))
	}

	argv, cleanup, err := shellArgvForOS("windows", long, scratch)
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

// TestShellArgvForOSWindowsShortCommandStaysInline pins the non-regression
// behavior: short Windows verification commands continue to use the existing
// cmd.exe /C <command> shape so we do not introduce unnecessary script
// materialization or extra cleanup.
func TestShellArgvForOSWindowsShortCommandStaysInline(t *testing.T) {
	argv, cleanup, err := shellArgvForOS("windows", "go test ./...", "")
	if err != nil {
		t.Fatal(err)
	}
	if cleanup != nil {
		t.Fatal("short Windows commands must not produce a cleanup function")
	}
	if len(argv) != 3 || argv[0] != "cmd.exe" || argv[1] != "/C" || argv[2] != "go test ./..." {
		t.Fatalf("short Windows shellArgv mismatch, got %#v", argv)
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
