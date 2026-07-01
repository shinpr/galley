package profilecmd

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

// AC1 (result-side): When required_checks.shell_path is set to a recognized
// shell executable and required_checks.shell is unset, the resolver infers
// the shell invocation style from the executable name and uses the path
// verbatim without consulting discovery hooks.
func TestShellArgvForOSInferShellKindFromShellPathBasenameWithoutExplicitShell(t *testing.T) {
	cases := []struct {
		name       string
		goos       string
		shellPath  string
		wantKind   string
		wantArgv0  string
		wantArgvOp string // ".sh" or ".cmd" or ".ps1" suffix for Windows; "-c" for Unix
	}{
		{name: "WindowsBashStandalone", goos: "windows", shellPath: `C:\opt\bash.exe`, wantKind: "bash", wantArgv0: `C:\opt\bash.exe`, wantArgvOp: ".sh"},
		{name: "WindowsCmdStandalone", goos: "windows", shellPath: `D:\alt\cmd.exe`, wantKind: "cmd", wantArgv0: `D:\alt\cmd.exe`, wantArgvOp: ".cmd"},
		{name: "WindowsPowerShellStandalone", goos: "windows", shellPath: `C:\tools\powershell.exe`, wantKind: "powershell", wantArgv0: `C:\tools\powershell.exe`, wantArgvOp: ".ps1"},
		{name: "WindowsPwshStandalone", goos: "windows", shellPath: `C:\tools\pwsh.exe`, wantKind: "pwsh", wantArgv0: `C:\tools\pwsh.exe`, wantArgvOp: ".ps1"},
		{name: "UnixBashStandalone", goos: "linux", shellPath: "/opt/galley/bash", wantKind: "bash", wantArgv0: "/opt/galley/bash", wantArgvOp: "-c"},
		{name: "UnixShStandalone", goos: "linux", shellPath: "/opt/galley/sh", wantKind: "sh", wantArgv0: "/opt/galley/sh", wantArgvOp: "-c"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			oldLook := lookPath
			oldStat := statFile
			var lookCalls, statCalls int
			lookPath = func(string) (string, error) { lookCalls++; return "", os.ErrNotExist }
			statFile = func(string) (os.FileInfo, error) { statCalls++; return nil, os.ErrNotExist }
			defer func() { lookPath = oldLook; statFile = oldStat }()

			argv, cleanup, shell, err := shellArgvForOS(tc.goos, "echo ok", t.TempDir(), "", tc.shellPath)
			if err != nil {
				t.Fatal(err)
			}
			if cleanup != nil {
				defer cleanup()
			}
			if shell != tc.wantKind {
				t.Fatalf("resolved shell got %q, want %q (inferred from %s basename)", shell, tc.wantKind, tc.shellPath)
			}
			if argv[0] != tc.wantArgv0 {
				t.Fatalf("argv[0] got %q, want %q (verbatim shell_path)", argv[0], tc.wantArgv0)
			}
			if lookCalls != 0 || statCalls != 0 {
				t.Fatalf("shell_path inference must skip discovery hooks (look=%d stat=%d)", lookCalls, statCalls)
			}
			if tc.goos == "windows" {
				if !strings.HasSuffix(argv[len(argv)-1], tc.wantArgvOp) {
					t.Fatalf("argv[-1] %q must end with %q", argv[len(argv)-1], tc.wantArgvOp)
				}
			} else {
				if argv[1] != tc.wantArgvOp {
					t.Fatalf("argv[1] got %q, want %q", argv[1], tc.wantArgvOp)
				}
			}
		})
	}
}

// AC2: When both required_checks.shell and required_checks.shell_path are
// set and shell_path's basename is recognized, shell_path's inferred kind
// wins so Galley does NOT invoke that executable with an incompatible
// invocation style from `shell`. This covers the cross-style conflict case
// (e.g. shell=cmd while shell_path points at bash.exe).
func TestShellArgvForOSShellPathInferredKindOverridesIncompatibleConfiguredShell(t *testing.T) {
	cases := []struct {
		name             string
		goos             string
		configuredShell  string
		shellPath        string
		wantResolvedKind string
		wantInvocation   string
	}{
		{
			name:             "CmdConfigured_BashPath_UsesBashStyle",
			goos:             "windows",
			configuredShell:  "cmd",
			shellPath:        `C:\tools\Git\bin\bash.exe`,
			wantResolvedKind: "bash",
			wantInvocation:   ".sh",
		},
		{
			name:             "BashConfigured_CmdPath_UsesCmdStyle",
			goos:             "windows",
			configuredShell:  "bash",
			shellPath:        `C:\Windows\System32\cmd.exe`,
			wantResolvedKind: "cmd",
			wantInvocation:   ".cmd",
		},
		{
			name:             "PowerShellConfigured_PwshPath_UsesPwshStyle",
			goos:             "windows",
			configuredShell:  "powershell",
			shellPath:        `C:\Program Files\PowerShell\7\pwsh.exe`,
			wantResolvedKind: "pwsh",
			wantInvocation:   ".ps1",
		},
		{
			name:             "UnixBashConfigured_ShPath_UsesShStyle",
			goos:             "linux",
			configuredShell:  "bash",
			shellPath:        `/bin/sh`,
			wantResolvedKind: "sh",
			wantInvocation:   "-c",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			oldLook := lookPath
			oldStat := statFile
			lookPath = func(string) (string, error) { return "", os.ErrNotExist }
			statFile = func(string) (os.FileInfo, error) { return nil, os.ErrNotExist }
			defer func() { lookPath = oldLook; statFile = oldStat }()

			argv, cleanup, shell, err := shellArgvForOS(tc.goos, "echo ok", t.TempDir(), tc.configuredShell, tc.shellPath)
			if err != nil {
				t.Fatal(err)
			}
			if cleanup != nil {
				defer cleanup()
			}
			if shell != tc.wantResolvedKind {
				t.Fatalf("resolved shell got %q, want %q (shell_path basename must win over configured shell %q)", shell, tc.wantResolvedKind, tc.configuredShell)
			}
			if argv[0] != tc.shellPath {
				t.Fatalf("argv[0] got %q, want %q (verbatim shell_path)", argv[0], tc.shellPath)
			}
			if tc.goos == "windows" {
				if !strings.HasSuffix(argv[len(argv)-1], tc.wantInvocation) {
					t.Fatalf("argv[-1] %q must end with %q (invocation style must match resolved kind)", argv[len(argv)-1], tc.wantInvocation)
				}
			} else if len(argv) < 2 || argv[1] != tc.wantInvocation {
				t.Fatalf("argv got %#v, want argv[1]=%q (invocation style must match resolved kind)", argv, tc.wantInvocation)
			}
		})
	}
}

// AC3 (result-side): When required_checks.shell_path uses an UNRECOGNIZED
// executable name, the resolver falls back to the configured shell kind for
// invocation style while still launching the supplied executable path.
func TestShellArgvForOSUnrecognizedShellPathFallsBackToConfiguredShellKindMetadata(t *testing.T) {
	cases := []struct {
		name             string
		goos             string
		configuredShell  string
		shellPath        string
		wantResolvedKind string
	}{
		{name: "UnixCustomBash", goos: "linux", configuredShell: "bash", shellPath: "/opt/galley/custom-bash", wantResolvedKind: "bash"},
		{name: "WindowsCustomPwsh", goos: "windows", configuredShell: "pwsh", shellPath: `C:\tools\custom-pwsh.exe`, wantResolvedKind: "pwsh"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			oldLook := lookPath
			oldStat := statFile
			lookPath = func(string) (string, error) { return "", os.ErrNotExist }
			statFile = func(string) (os.FileInfo, error) { return nil, os.ErrNotExist }
			defer func() { lookPath = oldLook; statFile = oldStat }()

			argv, cleanup, shell, err := shellArgvForOS(tc.goos, "echo ok", t.TempDir(), tc.configuredShell, tc.shellPath)
			if err != nil {
				t.Fatal(err)
			}
			if cleanup != nil {
				defer cleanup()
			}
			if shell != tc.wantResolvedKind {
				t.Fatalf("resolved shell got %q, want %q (fallback to configured shell when basename is unrecognized)", shell, tc.wantResolvedKind)
			}
			if argv[0] != tc.shellPath {
				t.Fatalf("argv[0] got %q, want %q (verbatim shell_path)", argv[0], tc.shellPath)
			}
		})
	}
}

// AC3 (result-side, defense-in-depth): If profile validation is bypassed and
// an unrecognized shell_path is paired with an unset/auto shell, the
// resolver itself must surface a clear error rather than silently picking a
// shell style. This guards the runtime even when a profile is loaded from
// an older or hand-edited source.
func TestShellArgvForOSUnrecognizedShellPathWithoutConfiguredShellReturnsError(t *testing.T) {
	cases := []struct {
		name            string
		configuredShell string
	}{
		{name: "EmptyShell", configuredShell: ""},
		{name: "AutoShell", configuredShell: "auto"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			oldLook := lookPath
			oldStat := statFile
			lookPath = func(string) (string, error) { return "", os.ErrNotExist }
			statFile = func(string) (os.FileInfo, error) { return nil, os.ErrNotExist }
			defer func() { lookPath = oldLook; statFile = oldStat }()

			_, _, _, err := shellArgvForOS("linux", "echo ok", t.TempDir(), tc.configuredShell, "/opt/galley/custom-bash")
			if err == nil {
				t.Fatal("expected error: unrecognized shell_path without fallback shell kind")
			}
			if !strings.Contains(err.Error(), "required_checks.shell_path") {
				t.Fatalf("error must name required_checks.shell_path, got %q", err.Error())
			}
		})
	}
}

// AC4: When required_checks.shell is bash on Windows and no shell_path is
// set, the resolver must prefer standard Git for Windows Bash discovery so
// PATH lookup cannot silently select the WSL launcher, WindowsApps shim,
// MSYS2, or other non-standard Bash installs. Without this, a default
// argv[0] of just "bash" would invite PATH to choose the WSL launcher.
func TestShellArgvForOSWindowsExplicitBashPrefersGitForWindowsOverWSL(t *testing.T) {
	cases := []struct {
		name      string
		stdPath   string
		gitPath   string
		wantBin   string
		statKeyFB bool // true: stat sees backslash path; false: stat sees forward-slash inferred path
	}{
		{name: "ProgramFiles_bin", stdPath: `C:\Program Files\Git\bin\bash.exe`, wantBin: `C:\Program Files\Git\bin\bash.exe`, statKeyFB: true},
		{name: "ProgramFiles_usr_bin", stdPath: `C:\Program Files\Git\usr\bin\bash.exe`, wantBin: `C:\Program Files\Git\usr\bin\bash.exe`, statKeyFB: true},
		{name: "PortableGitFromGitExe", gitPath: `C:\PortableGit\cmd\git.exe`, wantBin: `C:/PortableGit/bin/bash.exe`, statKeyFB: false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			oldLook := lookPath
			oldStat := statFile
			lookPath = func(file string) (string, error) {
				switch file {
				case "bash.exe", "bash":
					// PATH bash is always the WSL launcher to confirm Git for
					// Windows discovery wins over PATH lookup.
					return `C:\Windows\System32\bash.exe`, nil
				case "git.exe", "git":
					if tc.gitPath != "" {
						return tc.gitPath, nil
					}
					return "", os.ErrNotExist
				}
				return "", os.ErrNotExist
			}
			statFile = func(name string) (os.FileInfo, error) {
				if tc.statKeyFB && name == tc.stdPath {
					return fakeFileInfo{}, nil
				}
				if !tc.statKeyFB && name == tc.wantBin {
					return fakeFileInfo{}, nil
				}
				return nil, os.ErrNotExist
			}
			defer func() { lookPath = oldLook; statFile = oldStat }()

			argv, cleanup, shell, err := shellArgvForOS("windows", "grep -F ok proof.txt", t.TempDir(), "bash", "")
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
				t.Fatalf("argv[0] got %q, want %q (Git for Windows discovery must win over WSL launcher PATH lookup)", argv[0], tc.wantBin)
			}
		})
	}
}

// AC4 fallback: When required_checks.shell is bash on Windows with no
// shell_path and no Git for Windows Bash is discoverable, the resolver must
// NOT silently fall back to a bare "bash" executable. Bare "bash" would let
// exec-time PATH lookup pick up the very PATH-discovered Bash entries that
// discoverWindowsBash just rejected (WSL launcher, WindowsApps shim, MSYS2,
// Cygwin, Scoop, Chocolatey-managed Bashes), defeating the rejection. The
// resolver instead surfaces a clear error that names
// required_checks.shell_path as the explicit override the operator must set
// to opt in to a specific non-standard Bash.
func TestShellArgvForOSWindowsExplicitBashErrorsWhenNoStandardBashDiscoverable(t *testing.T) {
	oldLook := lookPath
	oldStat := statFile
	lookPath = func(string) (string, error) { return "", os.ErrNotExist }
	statFile = func(string) (os.FileInfo, error) { return nil, os.ErrNotExist }
	defer func() { lookPath = oldLook; statFile = oldStat }()

	argv, cleanup, _, err := shellArgvForOS("windows", "grep -F ok proof.txt", t.TempDir(), "bash", "")
	if cleanup != nil {
		defer cleanup()
	}
	if err == nil {
		t.Fatalf("expected resolver error when no standard Git for Windows Bash is discoverable; got argv %#v", argv)
	}
	if !strings.Contains(err.Error(), "required_checks.shell_path") {
		t.Fatalf("error must name required_checks.shell_path as the override path, got %q", err.Error())
	}
	if len(argv) > 0 && argv[0] == "bash" {
		t.Fatalf("resolver must never return a bare \"bash\" executable that PATH could resolve to a rejected entry; got argv[0]=%q", argv[0])
	}
}

// AC4 regression: When PATH-discovered bash points at a rejected non-standard
// install (WSL launcher, WindowsApps shim, MSYS2, Cygwin, Scoop) and no
// standard Git for Windows Bash is discoverable, the resolver must error and
// must NOT return argv[0] == "bash". A bare "bash" argv would defeat the
// rejection because exec-time PATH lookup would resolve "bash" back to the
// same rejected entry that discoverWindowsBash refused to auto-select.
// Operators that need to use one of these non-standard Bashes must opt in via
// required_checks.shell_path.
func TestShellArgvForOSWindowsExplicitBashErrorsWhenOnlyNonStandardBashOnPath(t *testing.T) {
	cases := []struct {
		name     string
		pathBash string
	}{
		{name: "WSLLauncher", pathBash: `C:\Windows\System32\bash.exe`},
		{name: "WindowsAppsShim", pathBash: `C:\Users\runner\AppData\Local\Microsoft\WindowsApps\bash.exe`},
		{name: "MSYS2", pathBash: `C:\msys64\usr\bin\bash.exe`},
		{name: "Cygwin", pathBash: `C:\cygwin64\bin\bash.exe`},
		{name: "ScoopGit", pathBash: `C:\Users\runner\scoop\apps\git\current\bin\bash.exe`},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			oldLook := lookPath
			oldStat := statFile
			lookPath = func(file string) (string, error) {
				if file == "bash.exe" || file == "bash" {
					return tc.pathBash, nil
				}
				return "", os.ErrNotExist
			}
			statFile = func(string) (os.FileInfo, error) { return nil, os.ErrNotExist }
			defer func() { lookPath = oldLook; statFile = oldStat }()

			argv, cleanup, _, err := shellArgvForOS("windows", "grep -F ok proof.txt", t.TempDir(), "bash", "")
			if cleanup != nil {
				defer cleanup()
			}
			if err == nil {
				t.Fatalf("expected resolver error when PATH bash is a rejected non-standard install (%s); got argv %#v", tc.pathBash, argv)
			}
			if !strings.Contains(err.Error(), "required_checks.shell_path") {
				t.Fatalf("error must name required_checks.shell_path as the override, got %q", err.Error())
			}
			if len(argv) > 0 && argv[0] == "bash" {
				t.Fatalf("resolver must NOT return argv[0]==\"bash\" because PATH would resolve it to the rejected entry %q; got argv %#v", tc.pathBash, argv)
			}
			if len(argv) > 0 && argv[0] == tc.pathBash {
				t.Fatalf("resolver must NOT return the rejected PATH entry %q as argv[0]; got argv %#v", tc.pathBash, argv)
			}
		})
	}
}
