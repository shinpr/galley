package runner

import (
	"sort"
	"strings"
	"testing"
)

// TestRestrictedEnvUnixDefaults covers AC3: the historical Unix-oriented
// allowlist (PATH/HOME/LANG/...), LC_* preservation, caller-supplied extras,
// and omission of unrelated parent environment values.
func TestRestrictedEnvUnixDefaults(t *testing.T) {
	parent := []string{
		"PATH=/usr/bin",
		"HOME=/home/u",
		"LANG=en_US.UTF-8",
		"LC_ALL=en_US.UTF-8",
		"LC_TIME=POSIX",
		"FOO=bar",            // unrelated
		"AWS_SECRET_KEY=xxx", // unrelated
		"=novalue",           // empty key
		"NOVAR",              // malformed entry without "="
	}
	got := restrictedEnvFromOS("linux", parent, "EXTRA=value", "GALLEY_GUARD=on")
	want := []string{
		"PATH=/usr/bin",
		"HOME=/home/u",
		"LANG=en_US.UTF-8",
		"LC_ALL=en_US.UTF-8",
		"LC_TIME=POSIX",
		"EXTRA=value",
		"GALLEY_GUARD=on",
	}
	assertSameEntries(t, got, want)
	for _, entry := range got {
		if strings.HasPrefix(entry, "FOO=") || strings.HasPrefix(entry, "AWS_SECRET_KEY=") {
			t.Errorf("unrelated env entry leaked into restricted env: %q", entry)
		}
	}
}

// TestRestrictedEnvUnixDoesNotInheritWindowsKeys also covers AC3: the
// Windows-only allowlist must not widen Unix behavior. A Linux/Darwin process
// with Windows-shaped environment keys should still see only the documented
// Unix allowlist (here, PATH).
func TestRestrictedEnvUnixDoesNotInheritWindowsKeys(t *testing.T) {
	parent := []string{
		"PATH=/usr/bin",
		"PATHEXT=.EXE",
		"WINDIR=C\\Windows",
		"USERPROFILE=C\\Users\\u",
		"APPDATA=C\\Users\\u\\AppData\\Roaming",
		"LOCALAPPDATA=C\\Users\\u\\AppData\\Local",
		"TEMP=C\\Temp",
		"TMP=C\\Tmp",
		"SYSTEMROOT=C\\Windows",
		"COMSPEC=C\\Windows\\System32\\cmd.exe",
	}
	got := restrictedEnvFromOS("linux", parent)
	assertSameEntries(t, got, []string{"PATH=/usr/bin"})
}

// TestRestrictedEnvWindowsPreservesWindowsKeys covers AC1: on Windows the
// documented Windows process environment keys (SYSTEMROOT, WINDIR, COMSPEC,
// PATHEXT, USERPROFILE, APPDATA, LOCALAPPDATA, TEMP, TMP) are preserved when
// present, alongside the historical allowlist (PATH/LANG/...), while unrelated
// values stay omitted.
func TestRestrictedEnvWindowsPreservesWindowsKeys(t *testing.T) {
	parent := []string{
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
	}
	got := restrictedEnvFromOS("windows", parent, "GALLEY_CLAUDE_GUARD_MODE=supervisor")
	want := []string{
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
		"GALLEY_CLAUDE_GUARD_MODE=supervisor",
	}
	assertSameEntries(t, got, want)
	for _, entry := range got {
		if strings.HasPrefix(entry, "FOO=") || strings.HasPrefix(entry, "AWS_SECRET_KEY=") {
			t.Errorf("unrelated env entry leaked into restricted env: %q", entry)
		}
	}
}

// TestRestrictedEnvWindowsCaseInsensitiveKeys covers AC2: Windows env keys are
// case-insensitive, so parent environments using "Path", "SystemRoot",
// "ComSpec", "PathExt", "UserProfile", "AppData", "LocalAppData", "Temp",
// "Tmp", or "Windir" casing must be preserved. The original entry (including
// its original casing) is retained in the resulting environment so downstream
// processes observe the same key shape they would inherit from the OS.
func TestRestrictedEnvWindowsCaseInsensitiveKeys(t *testing.T) {
	parent := []string{
		"Path=C:\\Windows;C:\\Tools",
		"SystemRoot=C:\\Windows",
		"ComSpec=C:\\Windows\\System32\\cmd.exe",
		"PathExt=.COM;.EXE;.BAT;.CMD",
		"UserProfile=C:\\Users\\u",
		"AppData=C:\\Users\\u\\AppData\\Roaming",
		"LocalAppData=C:\\Users\\u\\AppData\\Local",
		"Temp=C:\\Tmp",
		"Tmp=C:\\Tmp2",
		"Windir=C:\\Windows",
		"NotAllowed=value",
	}
	got := restrictedEnvFromOS("windows", parent)
	wantContains := []string{
		"Path=C:\\Windows;C:\\Tools",
		"SystemRoot=C:\\Windows",
		"ComSpec=C:\\Windows\\System32\\cmd.exe",
		"PathExt=.COM;.EXE;.BAT;.CMD",
		"UserProfile=C:\\Users\\u",
		"AppData=C:\\Users\\u\\AppData\\Roaming",
		"LocalAppData=C:\\Users\\u\\AppData\\Local",
		"Temp=C:\\Tmp",
		"Tmp=C:\\Tmp2",
		"Windir=C:\\Windows",
	}
	assertSameEntries(t, got, wantContains)
	for _, entry := range got {
		if strings.HasPrefix(entry, "NotAllowed=") {
			t.Errorf("unrelated env entry leaked into restricted env: %q", entry)
		}
	}
}

// TestRestrictedEnvWindowsPreservesLCKeys ensures LC_* preservation still
// applies on Windows (with case-insensitive matching), supporting AC2/AC3
// interaction: turning on Windows-specific behavior must not regress LC_*
// inheritance.
func TestRestrictedEnvWindowsPreservesLCKeys(t *testing.T) {
	parent := []string{
		"LC_ALL=en_US.UTF-8",
		"Lc_Time=POSIX",
		"PATH=C:\\Windows",
		"OTHER=value",
	}
	got := restrictedEnvFromOS("windows", parent)
	assertSameEntries(t, got, []string{
		"LC_ALL=en_US.UTF-8",
		"Lc_Time=POSIX",
		"PATH=C:\\Windows",
	})
}

// TestRestrictedEnvExportedAPI is a smoke test ensuring the exported
// RestrictedEnv signature still produces inherited entries from the live
// parent environment for the current host, supporting AC4 (call sites
// continue to receive a usable env without contract changes).
func TestRestrictedEnvExportedAPI(t *testing.T) {
	t.Setenv("PATH", "/usr/bin")
	got := RestrictedEnv("EXTRA=ok")
	foundExtra := false
	for _, entry := range got {
		if entry == "EXTRA=ok" {
			foundExtra = true
			break
		}
	}
	if !foundExtra {
		t.Errorf("expected EXTRA=ok in RestrictedEnv output, got %v", got)
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
