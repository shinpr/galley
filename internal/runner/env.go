package runner

import (
	"os"
	"runtime"
	"strings"
)

// InheritedEnv returns the parent process environment, optionally overridden
// by caller-supplied extras, for Galley-launched model subprocesses (Claude
// and Codex executor, setup executor, acceptance skeleton creator, and
// supervisor adapters).
//
// Galley treats the parent process environment as the subprocess execution
// contract and does not curate an allowlist of inherited keys. Issue #75
// showed that hidden allowlist filtering breaks Windows AFK runs because the
// removed entries (SystemDrive, ProgramData, ChocolateyInstall, user-defined
// PATH augmentations, custom toolchain variables, etc.) cannot be safely
// reconstructed from a fixed list. Inheriting the parent environment as-is
// is the only mechanism that preserves the toolchains the operator already
// configured.
//
// AC1: every inherited entry whose key is non-empty and whose raw entry
// contains "=" is preserved exactly (the original key and value bytes are
// copied verbatim into the returned slice).
//
// AC2: each entry in extra overrides any inherited entry that shares the
// same key. Override matching is case-sensitive on Unix-style hosts and
// case-insensitive on Windows (where the OS itself treats environment keys
// case-insensitively). Extras are appended after the surviving inherited
// entries so the override behavior is deterministic for any Go-managed
// subprocess.
//
// AC5: callers that persist subprocess command plans strip the returned
// environment slice before writing run evidence; the slice itself is only
// used at process-launch time.
func InheritedEnv(extra ...string) []string {
	return inheritedEnvFromOS(runtime.GOOS, os.Environ(), extra...)
}

// RestrictedEnv is retained as a backward-compatible alias for InheritedEnv
// so existing internal call sites continue to compile without churn.
//
// Deprecated: use InheritedEnv. RestrictedEnv no longer filters parent
// environment entries through an allowlist; it returns the parent
// environment with caller-supplied extras applied as overrides.
func RestrictedEnv(extra ...string) []string {
	return InheritedEnv(extra...)
}

// inheritedEnvFromOS is the platform-parameterized implementation of
// InheritedEnv. It is exported within the package so tests can exercise the
// Windows-specific override casing without running on a Windows host.
//
// parentEnv is expected to be the parent process environment (os.Environ()
// in production). Entries with an empty key or no "=" separator are dropped,
// matching the contract documented on InheritedEnv (and matching Go's
// os/exec behavior, which silently ignores such malformed entries).
func inheritedEnvFromOS(goos string, parentEnv []string, extra ...string) []string {
	isWindows := goos == "windows"
	normalize := func(key string) string {
		if isWindows {
			return strings.ToUpper(key)
		}
		return key
	}

	// Collect extras first so the override key set is known before the
	// parent environment is filtered. Skip malformed extras (no "=" or
	// empty key) so the override contract documented on InheritedEnv is
	// strict about what counts as an override and so the returned slice
	// never contains a no-op extra.
	overrideKeys := make(map[string]struct{}, len(extra))
	cleanedExtras := make([]string, 0, len(extra))
	for _, entry := range extra {
		key, _, ok := strings.Cut(entry, "=")
		if !ok || key == "" {
			continue
		}
		overrideKeys[normalize(key)] = struct{}{}
		cleanedExtras = append(cleanedExtras, entry)
	}

	out := make([]string, 0, len(parentEnv)+len(cleanedExtras))
	for _, entry := range parentEnv {
		key, _, ok := strings.Cut(entry, "=")
		if !ok || key == "" {
			continue
		}
		if _, overridden := overrideKeys[normalize(key)]; overridden {
			continue
		}
		out = append(out, entry)
	}
	out = append(out, cleanedExtras...)
	return out
}
