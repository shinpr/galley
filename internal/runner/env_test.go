package runner

import (
	"sort"
	"strings"
	"testing"
)

// AC6: the historical allowlist tests are removed and replaced with
// inheritance-contract tests that exercise the Issue #75 environment shape
// on Unix and Windows: parent entries (including credential-like, Windows
// system, and arbitrary custom keys) must reach the constructed subprocess
// environment unchanged, and caller-supplied extras must override matching
// inherited entries by key.

// TestInheritedEnvUnixPassesEveryParentEntry covers AC1/AC6: on Unix-style
// hosts every parent process entry with a non-empty key and "=" separator is
// preserved exactly, including credential-like and unrelated values that the
// previous allowlist used to drop.
func TestInheritedEnvUnixPassesEveryParentEntry(t *testing.T) {
	parent := []string{
		"PATH=/usr/bin",
		"HOME=/home/u",
		"LANG=en_US.UTF-8",
		"LC_ALL=en_US.UTF-8",
		"LC_TIME=POSIX",
		"FOO=bar",
		"AWS_SECRET_KEY=xxx",
		"GITHUB_TOKEN=ghp_example",
		"=novalue", // empty key - must be dropped
		"NOVAR",    // no "=" - must be dropped
	}
	got := inheritedEnvFromOS("linux", parent, "EXTRA=value", "GALLEY_CLAUDE_GUARD_MODE=on")
	want := []string{
		"PATH=/usr/bin",
		"HOME=/home/u",
		"LANG=en_US.UTF-8",
		"LC_ALL=en_US.UTF-8",
		"LC_TIME=POSIX",
		"FOO=bar",
		"AWS_SECRET_KEY=xxx",
		"GITHUB_TOKEN=ghp_example",
		"EXTRA=value",
		"GALLEY_CLAUDE_GUARD_MODE=on",
	}
	assertSameEntries(t, got, want)
	for _, entry := range got {
		if strings.HasPrefix(entry, "=") {
			t.Errorf("empty-key entry leaked into inherited env: %q", entry)
		}
		if entry == "NOVAR" {
			t.Errorf("malformed entry without '=' leaked into inherited env: %q", entry)
		}
	}
}

// TestInheritedEnvUnixPassesWindowsShapedKeys covers AC6: keys that the
// previous Windows-only allowlist treated specially must inherit on Unix
// hosts too, because Galley no longer filters by key shape. A Linux process
// that happens to see "PATHEXT" or "WINDIR" in its parent environment must
// still pass them through.
func TestInheritedEnvUnixPassesWindowsShapedKeys(t *testing.T) {
	parent := []string{
		"PATH=/usr/bin",
		"PATHEXT=.EXE",
		"WINDIR=C\\Windows",
		"USERPROFILE=C\\Users\\u",
		"SystemDrive=C:",
		"ProgramData=C:\\ProgramData",
		"CustomVar=anything-the-user-set",
	}
	got := inheritedEnvFromOS("linux", parent)
	assertSameEntries(t, got, parent)
}

// TestInheritedEnvWindowsPassesEveryParentEntry covers AC1/AC3/AC6: every
// parent entry must inherit on Windows, including Windows system variables
// (SystemDrive, ProgramData, SYSTEMROOT, WINDIR, COMSPEC, PATHEXT,
// USERPROFILE, APPDATA, LOCALAPPDATA, TEMP, TMP), the historical Unix
// allowlist keys, credential-like values, and arbitrary custom entries.
func TestInheritedEnvWindowsPassesEveryParentEntry(t *testing.T) {
	parent := []string{
		"SystemDrive=C:",
		"ProgramData=C:\\ProgramData",
		"SYSTEMROOT=C:\\Windows",
		"WINDIR=C:\\Windows",
		"COMSPEC=C:\\Windows\\System32\\cmd.exe",
		"PATHEXT=.COM;.EXE;.BAT;.CMD",
		"USERPROFILE=C:\\Users\\u",
		"APPDATA=C:\\Users\\u\\AppData\\Roaming",
		"LOCALAPPDATA=C:\\Users\\u\\AppData\\Local",
		"TEMP=C:\\Users\\u\\AppData\\Local\\Temp",
		"TMP=C:\\Users\\u\\AppData\\Local\\Temp",
		"PATH=C:\\Windows;C:\\Program Files\\Git\\cmd",
		"LANG=en_US.UTF-8",
		"FOO=bar",
		"AWS_SECRET_KEY=xxx",
		"GITHUB_TOKEN=ghp_example",
		"ChocolateyInstall=C:\\ProgramData\\chocolatey",
		"Custom_Tool_Home=C:\\tools\\custom",
	}
	got := inheritedEnvFromOS("windows", parent, "GALLEY_CLAUDE_GUARD_MODE=supervisor")
	want := append([]string(nil), parent...)
	want = append(want, "GALLEY_CLAUDE_GUARD_MODE=supervisor")
	assertSameEntries(t, got, want)
}

// TestInheritedEnvWindowsCaseInsensitiveOverride covers AC2: on Windows the
// override match is case-insensitive because Windows environment keys are
// case-insensitive at the OS level. A caller-supplied "Path=..." override
// must replace a parent "PATH=..." entry rather than producing two
// conflicting Path-like entries.
func TestInheritedEnvWindowsCaseInsensitiveOverride(t *testing.T) {
	parent := []string{
		"PATH=C:\\Windows",
		"SystemRoot=C:\\Windows",
		"Custom=keep-me",
	}
	got := inheritedEnvFromOS("windows", parent, "Path=C:\\overridden", "SYSTEMROOT=C:\\OverrideWin")
	want := []string{
		"Custom=keep-me",
		"Path=C:\\overridden",
		"SYSTEMROOT=C:\\OverrideWin",
	}
	assertSameEntries(t, got, want)
}

// TestInheritedEnvUnixOverrideIsCaseSensitive covers AC2 on Unix: an extra
// with a different-cased key from a parent entry does not collide; only
// exact key matches override.
func TestInheritedEnvUnixOverrideIsCaseSensitive(t *testing.T) {
	parent := []string{
		"PATH=/usr/bin",
		"path=/lower",
	}
	got := inheritedEnvFromOS("linux", parent, "PATH=/override")
	want := []string{
		"path=/lower",
		"PATH=/override",
	}
	assertSameEntries(t, got, want)
}

// TestInheritedEnvOverrideSkipsMalformedExtras documents that an extra with
// no "=" or an empty key is silently dropped (matching os/exec semantics)
// and does not trigger an override against any inherited entry.
func TestInheritedEnvOverrideSkipsMalformedExtras(t *testing.T) {
	parent := []string{
		"PATH=/usr/bin",
		"HOME=/home/u",
	}
	got := inheritedEnvFromOS("linux", parent, "NO_EQUALS_HERE", "=novalue", "VALID=ok")
	want := []string{
		"PATH=/usr/bin",
		"HOME=/home/u",
		"VALID=ok",
	}
	assertSameEntries(t, got, want)
}

// TestInheritedEnvIssue75ShapeOnEveryHostOS covers AC3 explicitly: the
// SystemDrive, ProgramData, and an arbitrary custom entry from the Issue #75
// reproducing shape must reach the constructed subprocess environment
// unchanged when the host OS is linux, darwin, or windows.
func TestInheritedEnvIssue75ShapeOnEveryHostOS(t *testing.T) {
	parent := []string{
		"SystemDrive=C:",
		"ProgramData=C:\\ProgramData",
		"Galley_Issue_75=arbitrary-custom-value",
	}
	for _, goos := range []string{"linux", "darwin", "windows"} {
		t.Run(goos, func(t *testing.T) {
			got := inheritedEnvFromOS(goos, parent)
			assertSameEntries(t, got, parent)
		})
	}
}

// TestInheritedEnvExportedAPI is a smoke test for InheritedEnv (and the
// backward-compatible RestrictedEnv alias) against the live parent
// environment of the test process. It supports AC4: existing call sites
// continue to receive a usable env without contract changes.
func TestInheritedEnvExportedAPI(t *testing.T) {
	t.Setenv("PATH", "/usr/bin")
	t.Setenv("GALLEY_ISSUE_75_TEST_KEY", "ok")
	for _, name := range []string{"InheritedEnv", "RestrictedEnv"} {
		var got []string
		switch name {
		case "InheritedEnv":
			got = InheritedEnv("EXTRA=ok")
		case "RestrictedEnv":
			got = RestrictedEnv("EXTRA=ok")
		}
		var foundExtra, foundCustom bool
		for _, entry := range got {
			if entry == "EXTRA=ok" {
				foundExtra = true
			}
			if entry == "GALLEY_ISSUE_75_TEST_KEY=ok" {
				foundCustom = true
			}
		}
		if !foundExtra {
			t.Errorf("%s: expected EXTRA=ok in output", name)
		}
		if !foundCustom {
			t.Errorf("%s: expected custom parent entry GALLEY_ISSUE_75_TEST_KEY=ok to inherit", name)
		}
	}
}

func assertSameEntries(t *testing.T, got, want []string) {
	t.Helper()
	gotSorted := append([]string(nil), got...)
	wantSorted := append([]string(nil), want...)
	sort.Strings(gotSorted)
	sort.Strings(wantSorted)
	if len(gotSorted) != len(wantSorted) {
		t.Fatalf("entry count mismatch:\n  got  (%d): %v\n  want (%d): %v", len(gotSorted), gotSorted, len(wantSorted), wantSorted)
	}
	for i := range gotSorted {
		if gotSorted[i] != wantSorted[i] {
			t.Errorf("entry %d differs:\n  got:  %q\n  want: %q", i, gotSorted[i], wantSorted[i])
		}
	}
}
