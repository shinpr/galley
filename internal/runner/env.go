package runner

import (
	"os"
	"strings"
)

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

// RestrictedEnv returns a small inherited environment for model subprocesses.
func RestrictedEnv(extra ...string) []string {
	env := make([]string, 0, len(defaultInheritedEnv)+len(extra))
	for _, entry := range os.Environ() {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if defaultInheritedEnv[key] || strings.HasPrefix(key, "LC_") {
			env = append(env, entry)
		}
	}
	env = append(env, extra...)
	return env
}
