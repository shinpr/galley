package runner

import (
	"os"
	"runtime"
	"strings"
)

// defaultInheritedEnv lists the cross-platform environment keys that
// RestrictedEnv preserves from the parent process. Keys are matched
// case-sensitively on Unix-style hosts and case-insensitively on Windows
// (see restrictedEnvFromOS).
var defaultInheritedEnv = map[string]bool{
	"ANTHROPIC_API_KEY": true,
	"CODEX_HOME":        true,
	// GitHub token env vars are intentionally omitted; PR operations should use
	// the authenticated gh configuration instead of exposing tokens to model runs.
	"HOME":            true,
	"LANG":            true,
	"LOGNAME":         true,
	"OPENAI_API_KEY":  true,
	"PATH":            true,
	"SHELL":           true,
	"TERM":            true,
	"TMPDIR":          true,
	"USER":            true,
	"XDG_CONFIG_HOME": true,
	"XDG_CACHE_HOME":  true,
	"XDG_DATA_HOME":   true,
}

// windowsInheritedEnv lists the additional process environment keys that
// Windows requires for cmd.exe, .cmd shims, user-local tool discovery,
// temp files, and executable resolution. These keys are matched
// case-insensitively because Windows env keys themselves are case-insensitive
// and the parent process may surface them with any casing (e.g. "Path",
// "SystemRoot", "ComSpec").
var windowsInheritedEnv = map[string]bool{
	"SYSTEMROOT":   true,
	"WINDIR":       true,
	"COMSPEC":      true,
	"PATHEXT":      true,
	"USERPROFILE":  true,
	"APPDATA":      true,
	"LOCALAPPDATA": true,
	"TEMP":         true,
	"TMP":          true,
}

// RestrictedEnv returns a small inherited environment for model subprocesses.
// On Unix-style hosts it preserves the historical Unix-oriented allowlist
// (case-sensitive keys, LC_* preservation, and caller-supplied extras). On
// Windows it additionally preserves Windows process environment keys that
// cmd.exe, .cmd shims, user-local tool discovery, temp files, and executable
// resolution require, and matches all keys case-insensitively because Windows
// env keys are case-insensitive.
func RestrictedEnv(extra ...string) []string {
	return restrictedEnvFromOS(runtime.GOOS, os.Environ(), extra...)
}

// restrictedEnvFromOS is the platform-parameterized implementation of
// RestrictedEnv. It is exported within the package so tests can exercise the
// Windows-specific path without running on a Windows host.
func restrictedEnvFromOS(goos string, parentEnv []string, extra ...string) []string {
	isWindows := goos == "windows"
	env := make([]string, 0, len(parentEnv)+len(extra))
	for _, entry := range parentEnv {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if isWindows {
			upper := strings.ToUpper(key)
			if defaultInheritedEnv[upper] || windowsInheritedEnv[upper] || strings.HasPrefix(upper, "LC_") {
				env = append(env, entry)
			}
			continue
		}
		if defaultInheritedEnv[key] || strings.HasPrefix(key, "LC_") {
			env = append(env, entry)
		}
	}
	env = append(env, extra...)
	return env
}
