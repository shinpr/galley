// Package notify runs the daemon's opt-in, best-effort notification command
// hook after a task reaches a terminal published status.
//
// Trust boundary: the configured command string is operator-owned and is the
// only thing placed in the shell's argument vector. Every task-derived value
// (id, status, repo, summary, run dir) is untrusted content and is delivered
// exclusively through a JSON object on stdin and namespaced GALLEY_*
// environment variables. Task content is never concatenated into the command
// string, so shell metacharacters or environment-like names in a task summary
// or repo path cannot alter the executed command. This mirrors how the Claude
// and Codex runners already pass untrusted prompt content via stdin.
//
// Execution reuses internal/profilecmd shell resolution (the same machinery as
// required checks) so macOS, Linux, and Windows invocation paths are covered by
// one shell-selection contract, and internal/runner.RunCommand for the bounded
// timeout plus process-group kill. The hook is best-effort: a non-zero exit,
// start failure, timeout, or hang surfaces as a returned error for the caller
// to log; it must not affect the already-successful task state transition.
package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"time"

	"github.com/shinpr/galley/internal/profile"
	"github.com/shinpr/galley/internal/profilecmd"
	"github.com/shinpr/galley/internal/runner"
)

// DefaultTimeout bounds a single notification hook invocation. The hook is
// best-effort and off the task-state critical path, so the timeout is short
// enough to keep a slow or hanging notifier from holding daemon resources while
// still allowing a real network delivery (Slack webhook, ntfy, email) to
// complete. It is intentionally not operator-tunable in the first version to
// keep the daemon.yaml surface small.
const DefaultTimeout = 30 * time.Second

// summaryMaxRunes bounds the latest-summary field. The bound is measured in
// runes, not bytes, so multibyte summaries are truncated on a character
// boundary and never split a UTF-8 sequence.
const summaryMaxRunes = 280

// Event is the task-derived data passed to a notification hook. Every field is
// untrusted task content delivered only via stdin JSON and GALLEY_* env.
type Event struct {
	TaskID  string
	Status  string
	Repo    string
	Summary string
	RunDir  string
}

// payload is the stable stdin JSON contract handed to the hook command. The
// field names are a versioned contract: operator scripts read them, so they
// must stay stable across releases.
type payload struct {
	TaskID   string `json:"task_id"`
	Status   string `json:"status"`
	Repo     string `json:"repo"`
	Summary  string `json:"summary"`
	RunDir   string `json:"run_dir"`
	ShowHint string `json:"show_hint"`
}

// Options controls a single hook invocation. The zero value resolves GOOS to
// the host and Timeout to DefaultTimeout, which is what the daemon uses.
type Options struct {
	// GOOS overrides the target OS for shell resolution. Empty uses
	// runtime.GOOS. Tests set this to exercise non-host shell selection.
	GOOS string
	// Timeout overrides DefaultTimeout. Non-positive uses DefaultTimeout.
	Timeout time.Duration
	// ScratchDir is where the Windows shell wrapper script is materialized.
	// Empty lets profilecmd allocate (and clean up) a temp directory.
	ScratchDir string
	// Resolver injects host shell lookups for tests. The zero value uses the
	// real PATH and filesystem.
	Resolver profilecmd.Resolver
}

// stdinJSON renders the stable stdin contract for the hook command.
func (e Event) stdinJSON() (string, error) {
	body, err := json.Marshal(payload{
		TaskID:   e.TaskID,
		Status:   e.Status,
		Repo:     e.Repo,
		Summary:  truncateRunes(e.Summary, summaryMaxRunes),
		RunDir:   e.RunDir,
		ShowHint: e.ShowHint(),
	})
	if err != nil {
		return "", fmt.Errorf("encode notification payload: %w", err)
	}
	return string(body) + "\n", nil
}

// ShowHint is the inspection command an operator can run to view the task.
func (e Event) ShowHint() string {
	return "galley task show " + e.TaskID
}

// env returns the namespaced GALLEY_* environment mirror. Only Galley-owned
// names are produced, so task content can never determine a variable name and
// cannot shadow PATH, IFS, or similar.
func (e Event) env() []string {
	return []string{
		"GALLEY_TASK_ID=" + e.TaskID,
		"GALLEY_TASK_STATUS=" + e.Status,
		"GALLEY_REPO=" + e.Repo,
		"GALLEY_SUMMARY=" + truncateRunes(e.Summary, summaryMaxRunes),
		"GALLEY_RUN_DIR=" + e.RunDir,
	}
}

// BuildCommand resolves the shell argv and assembles the runner command plan
// for command and ev. It is separated from Run so injection-regression tests
// can assert the command plan (argv carries only the operator command; task
// content lives in stdin/env) without spawning a process. The returned cleanup
// is non-nil on platforms that materialize a wrapper script and must be called
// once the command has run.
func BuildCommand(command string, ev Event, opts Options) (runner.Command, func(), error) {
	goos := opts.GOOS
	if goos == "" {
		goos = runtime.GOOS
	}
	argv, cleanup, _, err := profilecmd.ShellArgvForOSWithResolver(goos, command, opts.ScratchDir, profile.RequiredCheckEnvironment{}, opts.Resolver)
	if err != nil {
		return runner.Command{}, nil, err
	}
	if cleanup == nil {
		cleanup = func() {}
	}
	stdin, err := ev.stdinJSON()
	if err != nil {
		cleanup()
		return runner.Command{}, nil, err
	}
	return runner.Command{
		Argv:      argv,
		Stdin:     stdin,
		EnvAppend: ev.env(),
	}, cleanup, nil
}

// Run executes the notification hook for ev, returning the subprocess result
// and any execution error. The error is non-nil on a non-zero exit, start
// failure, timeout, or forced kill; callers log and continue because the hook
// is best-effort and must not affect task state.
func Run(ctx context.Context, command string, ev Event, opts Options) (runner.RunResult, error) {
	cmd, cleanup, err := BuildCommand(command, ev, opts)
	if err != nil {
		return runner.RunResult{}, err
	}
	defer cleanup()
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return runner.RunCommand(ctx, cmd, runner.RunOptions{Timeout: timeout})
}

// truncateRunes keeps at most max runes of content, appending a single-rune
// ellipsis when content was dropped so a downstream reader can tell the summary
// was clipped (a clipped result is therefore up to max+1 runes long).
func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "…"
}
