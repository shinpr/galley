package result

import (
	"os"
	"strings"
	"testing"
)

// AC1: When Galley resolves the required-check shell on Windows with
// required_checks.shell unset or "auto", the system shall prefer standard
// Git for Windows Bash installs over PATH-discovered bash.exe, including
// C:\Program Files\Git\bin\bash.exe, C:\Program Files\Git\usr\bin\bash.exe,
// and Git Bash inferred from git.exe.
func TestShellArgvForOSWindowsAutoPrefersStandardGitForWindowsInstalls(t *testing.T) {
	cases := []struct {
		name      string
		stdPath   string // standard install path returned by statFile
		gitPath   string // git.exe path returned by lookPath, "" means no git
		wantBin   string // expected resolved bash binary
		statFromB bool   // whether statFile recognizes the path with backslashes (true) or forward slashes (false)
	}{
		{
			name:      "ProgramFiles_bin_bash",
			stdPath:   `C:\Program Files\Git\bin\bash.exe`,
			wantBin:   `C:\Program Files\Git\bin\bash.exe`,
			statFromB: true,
		},
		{
			name:      "ProgramFiles_usr_bin_bash",
			stdPath:   `C:\Program Files\Git\usr\bin\bash.exe`,
			wantBin:   `C:\Program Files\Git\usr\bin\bash.exe`,
			statFromB: true,
		},
		{
			name:      "InferredFromGitExe",
			gitPath:   `C:\PortableGit\cmd\git.exe`,
			wantBin:   `C:/PortableGit/bin/bash.exe`,
			statFromB: false,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			oldLook := lookPath
			oldStat := statFile
			// PATH always exposes a non-standard WindowsApps bash to confirm
			// the standard install wins.
			lookPath = func(file string) (string, error) {
				switch file {
				case "bash.exe", "bash":
					return `C:\Users\runner\AppData\Local\Microsoft\WindowsApps\bash.exe`, nil
				case "git.exe", "git":
					if tc.gitPath != "" {
						return tc.gitPath, nil
					}
					return "", os.ErrNotExist
				}
				return "", os.ErrNotExist
			}
			statFile = func(name string) (os.FileInfo, error) {
				if tc.statFromB && name == tc.stdPath {
					return fakeFileInfo{}, nil
				}
				if !tc.statFromB && name == tc.wantBin {
					return fakeFileInfo{}, nil
				}
				return nil, os.ErrNotExist
			}
			defer func() {
				lookPath = oldLook
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
			if argv[0] != tc.wantBin {
				t.Fatalf("argv[0] got %q, want %q", argv[0], tc.wantBin)
			}
			if !strings.HasSuffix(argv[1], ".sh") {
				t.Fatalf("argv[1] should reference a .sh script, got %q", argv[1])
			}
		})
	}
}

// AC2: When PATH contains WSL launcher Bash entries such as
// C:\Windows\System32\bash.exe or a WindowsApps bash.exe, the Windows
// auto-discovery path shall NOT select those entries as Git Bash. If no
// usable Git Bash is found, Galley shall fall back to cmd.exe.
func TestShellArgvForOSWindowsAutoRejectsWSLLauncherBashAndFallsBackToCmd(t *testing.T) {
	t.Run("WSLSystem32FallsBackToCmd", func(t *testing.T) {
		oldLook := lookPath
		oldStat := statFile
		lookPath = func(file string) (string, error) {
			if file == "bash.exe" || file == "bash" {
				return `C:\Windows\System32\bash.exe`, nil
			}
			return "", os.ErrNotExist
		}
		statFile = func(name string) (os.FileInfo, error) { return nil, os.ErrNotExist }
		defer func() { lookPath = oldLook; statFile = oldStat }()

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
		if argv[0] != "cmd.exe" {
			t.Fatalf("argv[0] got %q, want cmd.exe", argv[0])
		}
	})
	t.Run("NonStandardMSYS2PathFallsBackToCmd", func(t *testing.T) {
		// Regression: PATH-discovered bash that points at a non-standard,
		// non-Git-for-Windows install (here MSYS2) must NOT be auto-selected
		// because Galley cannot guarantee it is Git Bash. No standard install
		// path is visible to statFile and no git.exe is on PATH, so the
		// resolver must fall back to cmd.exe rather than silently switching
		// required checks to MSYS2's bash.
		oldLook := lookPath
		oldStat := statFile
		lookPath = func(file string) (string, error) {
			if file == "bash.exe" || file == "bash" {
				return `C:\msys64\usr\bin\bash.exe`, nil
			}
			return "", os.ErrNotExist
		}
		statFile = func(name string) (os.FileInfo, error) { return nil, os.ErrNotExist }
		defer func() { lookPath = oldLook; statFile = oldStat }()

		argv, cleanup, shell, err := shellArgvForOS("windows", "go test ./...", t.TempDir(), "", "")
		if err != nil {
			t.Fatal(err)
		}
		if cleanup != nil {
			defer cleanup()
		}
		if shell != "cmd" {
			t.Fatalf("resolved shell got %q, want cmd (non-standard MSYS2 bash must not be auto-selected)", shell)
		}
		if argv[0] != "cmd.exe" {
			t.Fatalf("argv[0] got %q, want cmd.exe", argv[0])
		}
	})
	t.Run("WindowsAppsShimFallsBackToCmd", func(t *testing.T) {
		oldLook := lookPath
		oldStat := statFile
		lookPath = func(file string) (string, error) {
			if file == "bash.exe" || file == "bash" {
				return `C:\Users\runner\AppData\Local\Microsoft\WindowsApps\bash.exe`, nil
			}
			return "", os.ErrNotExist
		}
		statFile = func(name string) (os.FileInfo, error) { return nil, os.ErrNotExist }
		defer func() { lookPath = oldLook; statFile = oldStat }()

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
		if argv[0] != "cmd.exe" {
			t.Fatalf("argv[0] got %q, want cmd.exe", argv[0])
		}
	})
	t.Run("WSLLauncherWithStandardInstallStillPrefersStandard", func(t *testing.T) {
		oldLook := lookPath
		oldStat := statFile
		lookPath = func(file string) (string, error) {
			if file == "bash.exe" || file == "bash" {
				return `C:\Windows\System32\bash.exe`, nil
			}
			return "", os.ErrNotExist
		}
		statFile = func(name string) (os.FileInfo, error) {
			if name == `C:\Program Files\Git\bin\bash.exe` {
				return fakeFileInfo{}, nil
			}
			return nil, os.ErrNotExist
		}
		defer func() { lookPath = oldLook; statFile = oldStat }()

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
		if argv[0] != `C:\Program Files\Git\bin\bash.exe` {
			t.Fatalf("argv[0] got %q, want standard Git for Windows bash", argv[0])
		}
	})
}

// AC3 (result-side): When required_checks.shell_path is set with an
// explicit non-auto required_checks.shell, Galley uses that exact
// executable path for required-check execution and skips shell discovery.
func TestShellArgvForOSWindowsUsesExplicitRequiredCheckShellPathWithoutDiscovery(t *testing.T) {
	cases := []struct {
		name      string
		shell     string
		shellPath string
		wantArgv0 string
	}{
		{name: "CustomBash", shell: "bash", shellPath: `C:\custom\bash.exe`, wantArgv0: `C:\custom\bash.exe`},
		{name: "CustomPwsh", shell: "pwsh", shellPath: `C:\tools\pwsh.exe`, wantArgv0: `C:\tools\pwsh.exe`},
		{name: "CustomPowerShell", shell: "powershell", shellPath: `C:\tools\powershell.exe`, wantArgv0: `C:\tools\powershell.exe`},
		{name: "CustomCmd", shell: "cmd", shellPath: `D:\alt\cmd.exe`, wantArgv0: `D:\alt\cmd.exe`},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			oldLook := lookPath
			oldStat := statFile
			var lookCalls, statCalls int
			lookPath = func(string) (string, error) {
				lookCalls++
				return "", os.ErrNotExist
			}
			statFile = func(string) (os.FileInfo, error) {
				statCalls++
				return nil, os.ErrNotExist
			}
			defer func() { lookPath = oldLook; statFile = oldStat }()

			argv, cleanup, shell, err := shellArgvForOS("windows", "echo ok", t.TempDir(), tc.shell, tc.shellPath)
			if err != nil {
				t.Fatal(err)
			}
			if cleanup != nil {
				defer cleanup()
			}
			if shell != tc.shell {
				t.Fatalf("resolved shell got %q, want %q", shell, tc.shell)
			}
			if argv[0] != tc.wantArgv0 {
				t.Fatalf("argv[0] got %q, want %q (verbatim shell_path)", argv[0], tc.wantArgv0)
			}
			if lookCalls != 0 {
				t.Fatalf("lookPath was consulted %d times; explicit shell_path must skip discovery", lookCalls)
			}
			if statCalls != 0 {
				t.Fatalf("statFile was consulted %d times; explicit shell_path must skip discovery", statCalls)
			}
		})
	}
}

// AC3 (result-side, non-Windows): Explicit shell_path is also honored on
// non-Windows hosts so the field's behavior matches the public contract
// regardless of goos. Discovery hooks are not consulted because there is
// no auto path to take.
func TestShellArgvForOSExplicitRequiredCheckShellPathHonoredOnUnix(t *testing.T) {
	oldLook := lookPath
	oldStat := statFile
	var lookCalls, statCalls int
	lookPath = func(string) (string, error) { lookCalls++; return "", os.ErrNotExist }
	statFile = func(string) (os.FileInfo, error) { statCalls++; return nil, os.ErrNotExist }
	defer func() { lookPath = oldLook; statFile = oldStat }()

	argv, cleanup, shell, err := shellArgvForOS("linux", "echo ok", "", "bash", "/opt/galley/custom-bash")
	if err != nil {
		t.Fatal(err)
	}
	if cleanup != nil {
		t.Fatal("non-Windows resolution must not produce a cleanup function")
	}
	if shell != "bash" {
		t.Fatalf("resolved shell got %q, want bash", shell)
	}
	if argv[0] != "/opt/galley/custom-bash" {
		t.Fatalf("argv[0] got %q, want /opt/galley/custom-bash", argv[0])
	}
	if lookCalls != 0 || statCalls != 0 {
		t.Fatalf("discovery hooks must not be consulted on Unix when shell_path is set (look=%d stat=%d)", lookCalls, statCalls)
	}
}

// AC6: Required-check run evidence and failure reasons must record both
// the resolved shell kind and the executable path. This covers explicit
// shell_path, auto-resolved Git Bash on Windows, and cmd.exe fallback.
func TestVerificationEvidenceRecordsResolvedShellKindAndExecutablePath(t *testing.T) {
	cases := []struct {
		name      string
		run       verificationRun
		wantShell string
		wantBin   string
		failure   bool
	}{
		{
			name:      "ExplicitCustomBashPasses",
			run:       verificationRun{command: "go test ./...", shell: "bash", shellBin: `C:\custom\bash.exe`},
			wantShell: "shell=bash",
			wantBin:   `bin=C:\custom\bash.exe`,
		},
		{
			name:      "ExplicitCustomPowerShellPasses",
			run:       verificationRun{command: "Get-Item .", shell: "powershell", shellBin: `C:\tools\powershell.exe`},
			wantShell: "shell=powershell",
			wantBin:   `bin=C:\tools\powershell.exe`,
		},
		{
			name:      "AutoResolvedGitBashFails",
			run:       verificationRun{command: "grep -F ok proof.txt", shell: "bash", shellBin: `C:\Program Files\Git\bin\bash.exe`, err: os.ErrPermission},
			wantShell: "shell=bash",
			wantBin:   `bin=C:\Program Files\Git\bin\bash.exe`,
			failure:   true,
		},
		{
			name:      "CmdFallbackFails",
			run:       verificationRun{command: "grep -F ok proof.txt", shell: "cmd", shellBin: "cmd.exe", err: os.ErrPermission},
			wantShell: "shell=cmd",
			wantBin:   "bin=cmd.exe",
			failure:   true,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			reason := tc.run.reason()
			if !strings.Contains(reason, tc.wantShell) {
				t.Fatalf("reason missing %q: %q", tc.wantShell, reason)
			}
			if !strings.Contains(reason, tc.wantBin) {
				t.Fatalf("reason missing %q: %q", tc.wantBin, reason)
			}
			if tc.failure && !strings.Contains(reason, "failed") {
				t.Fatalf("failure reason should mark the run as failed, got %q", reason)
			}
			if !tc.failure && !strings.Contains(reason, "passed") {
				t.Fatalf("passing reason should mark the run as passed, got %q", reason)
			}
		})
	}
}
