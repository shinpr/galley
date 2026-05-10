package task

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// UnsafeCheckpointCommand applies the minimum mechanically enforceable
// validation recorded in design decision D7 for provider-authored checkpoint
// commands. It rejects empty commands, control characters / embedded NULs,
// absolute paths and ".." traversal in command tokens, explicit
// worktree-external write targets, shell features already disallowed by
// existing Galley policy, and a small set of obviously destructive patterns.
// Everything else is left to the fixed worktree cwd, environment constraints,
// recorded evidence, and the daemon-side accepted gate. It does not attempt
// unverifiable shell static analysis.
//
// It returns an empty string when the command passes the minimum
// command-surface checks, otherwise a short human-readable reason. The same
// helper is used both for static task YAML validation (preflight outputs and
// the creator command) and for runtime validation of creator-reported
// manifest checkpoint commands, so static and creator-provided commands are
// held to an identical bar.
func UnsafeCheckpointCommand(command string) string {
	trimmed := strings.TrimSpace(command)
	if trimmed == "" {
		return "command is empty"
	}
	for _, r := range trimmed {
		if r == 0x00 {
			return "contains NUL byte"
		}
		if r == '\n' || r == '\r' {
			return "must be a single line"
		}
		// Reject other ASCII control characters; they have no legitimate place
		// in an operator-reviewed one-line checkpoint command and complicate
		// evidence rendering.
		if r < 0x20 && r != '\t' {
			return "contains a control character"
		}
	}
	// Shell features already disallowed by Galley's command surface policy:
	// command substitution, process substitution, and backtick evaluation.
	// These can re-introduce arbitrary commands that bypass the rest of the
	// checks below, so they are rejected outright (D7).
	for _, needle := range []string{"$(", "${", "<(", ">(", "`"} {
		if strings.Contains(trimmed, needle) {
			return fmt.Sprintf("uses a disallowed shell feature %q", needle)
		}
	}
	tokens := strings.Fields(trimmed)
	for _, tok := range tokens {
		// Strip a single layer of surrounding quotes for inspection only.
		bare := strings.Trim(tok, `"'`)
		if bare == "" {
			continue
		}
		// Absolute paths in command tokens would let a checkpoint reach
		// outside the prepared worktree; reject them. The shell interpreter
		// path is supplied by Galley itself, not by the provider command, so
		// provider tokens never need to be absolute.
		if filepath.IsAbs(bare) {
			return fmt.Sprintf("contains absolute path token %q", tok)
		}
		// ".." traversal in any token (e.g. "../../etc/passwd" or
		// "go test ./../other") can escape the worktree; reject it.
		clean := filepath.Clean(bare)
		if clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(bare, "/../") || strings.HasSuffix(bare, "/..") {
			return fmt.Sprintf("contains parent-directory traversal token %q", tok)
		}
	}
	// Explicit worktree-external write targets via output redirection. We do
	// not parse the full shell grammar; we detect the common forms
	// `> /abs/path`, `>> /abs/path`, `> ../path`, and `2> ../path`.
	if reason := externalRedirectTarget(trimmed); reason != "" {
		return reason
	}
	dangerous := []string{
		"rm -rf /",
		":(){ :|:& };:",
		"mkfs",
		"dd if=/dev/zero",
		"shutdown",
		"reboot",
	}
	for _, needle := range dangerous {
		if strings.Contains(trimmed, needle) {
			return fmt.Sprintf("contains forbidden pattern %q", needle)
		}
	}
	return ""
}

var redirectTargetPattern = regexp.MustCompile(`(?:^|\s)[0-9]*>>?\s*("?'?)([^\s"']+)`)

// externalRedirectTarget detects redirection operators whose target is an
// absolute path or escapes the worktree via "..". It returns a non-empty
// reason string when such a target is found.
func externalRedirectTarget(command string) string {
	for _, m := range redirectTargetPattern.FindAllStringSubmatch(command, -1) {
		target := m[2]
		if target == "" || target == "&1" || target == "&2" {
			continue
		}
		if strings.HasPrefix(target, "&") {
			continue
		}
		if filepath.IsAbs(target) {
			return fmt.Sprintf("redirects output to an absolute path %q", target)
		}
		clean := filepath.Clean(target)
		if clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(target, "/../") {
			return fmt.Sprintf("redirects output outside the worktree to %q", target)
		}
	}
	return ""
}
