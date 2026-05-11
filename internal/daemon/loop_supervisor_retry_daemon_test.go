package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shinpr/galley/internal/task"
)

const fakeSupervisorAcceptedVerdict = `{"status":"accepted","summary":"codex accepted after stall","acceptance_gaps":[],"reviewed_files":["daemon-output.txt"],"acceptance_evidence":[{"ac_id":"AC1","evidence":["diff"]}],"findings":[],"residual_risks":[],"discussion_items":[],"confidence":"high","next_work_order":""}`

// writeStallThenAcceptCodexSupervisor returns a fake `codex` binary that, on
// its first invocation, reads the request and then produces no stdout/stderr
// (it just sleeps), so the daemon's real idle-timeout path SIGKILLs it. On
// every later invocation it writes the accepted verdict to the
// --output-last-message file and emits a stdout event so the supervisor
// adapter parses a normal success. The call-count marker lives next to the
// script so it is isolated from the executor workspace.
func writeStallThenAcceptCodexSupervisor(t *testing.T) string {
	t.Helper()
	return writeFakeCommand(t, "codex", `out=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --output-last-message) out="$2"; shift 2 ;;
    *) shift ;;
  esac
done
cat >/dev/null
marker="$(dirname "$0")/galley-supervisor-call"
if [ -f "$marker" ]; then
  printf '%s\n' '`+fakeSupervisorAcceptedVerdict+`' > "$out"
  printf '%s\n' '{"event":"done"}'
  exit 0
fi
: > "$marker"
sleep 5
`)
}

// writeAlwaysStallCodexSupervisor returns a fake `codex` binary that always
// reads the request and then stalls without output, so every supervisor try
// (the initial one plus the fixed retry budget) is killed by the daemon's
// real idle-timeout path.
func writeAlwaysStallCodexSupervisor(t *testing.T) string {
	t.Helper()
	return writeFakeCommand(t, "codex", "cat >/dev/null\nsleep 5\n")
}

func newDaemonRetryTask(t *testing.T) (root, taskPath string, repo string) {
	t.Helper()
	root = filepath.Join(t.TempDir(), ".agent-workflow")
	repo = initDaemonGitRepo(t)
	taskPath = filepath.Join(root, "tasks", "queued", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, taskPath, repo)
	return root, taskPath, repo
}

// TestDaemonSupervisorStallRetryRecoversWithinSameAttempt exercises AC-001
// end to end through the daemon supervisor loop: a built-in (codex) supervisor
// subprocess stalls and is killed by the real idle-timeout path on its first
// try, the loop retries the same evaluation under the same executor attempt
// directory using a supervisor-try-N subdir, recovers on the second try, and
// finalizes the task without starting a second executor attempt.
func TestDaemonSupervisorStallRetryRecoversWithinSameAttempt(t *testing.T) {
	root, taskPath, _ := newDaemonRetryTask(t)
	promptPath, schemaPath := writeDaemonPromptFiles(t)
	claudeBin := writeFakeClaude(t, "echo change > daemon-output.txt\necho '{\"status\":\"completed\",\"summary\":\"done\",\"files_modified\":[\"daemon-output.txt\"],\"acceptance_criteria\":[{\"id\":\"AC1\",\"status\":\"satisfied\",\"evidence\":[\"diff\"],\"notes\":\"done\"}],\"verification\":[],\"decisions\":[],\"risks\":[]}'\n")
	codexBin := writeStallThenAcceptCodexSupervisor(t)
	setLoopBudget(t, taskPath, 3)

	err := Run(context.Background(), Options{
		Root:               root,
		SystemPromptFile:   promptPath,
		JSONSchemaFile:     schemaPath,
		Once:               true,
		MaxConcurrentTasks: 1,
		Supervisor:         "codex",
		ClaudeBin:          claudeBin,
		CodexBin:           codexBin,
		IdleTimeout:        200 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("daemon run: %v", err)
	}

	doneTask, err := task.Load(filepath.Join(root, "tasks", "done", "task.yaml"))
	if err != nil {
		t.Fatalf("done task missing: %v", err)
	}
	if doneTask.Status != "accepted" {
		t.Fatalf("status got %q, want accepted", doneTask.Status)
	}
	// Exactly one executor attempt: the supervisor stall must be handled
	// inside attempt-1, never by spending a fresh executor attempt.
	if len(doneTask.Attempts) != 1 {
		t.Fatalf("attempts got %d, want 1 (supervisor retry must stay in attempt-1)", len(doneTask.Attempts))
	}
	if doneTask.Attempts[0].SupervisorVerdict != "accepted" {
		t.Fatalf("attempt verdict got %q, want accepted", doneTask.Attempts[0].SupervisorVerdict)
	}
	attemptGlob := filepath.Join(root, "runs", "*", "attempt-1")
	matches, err := filepath.Glob(attemptGlob)
	if err != nil || len(matches) != 1 {
		t.Fatalf("attempt-1 dir glob got %v (err=%v), want exactly one", matches, err)
	}
	attemptDir := matches[0]
	assertGlobCount(t, filepath.Join(root, "runs", "*", "attempt-2"), 0)
	// Retry-scoped artifacts: the killed first try and the recovered second
	// try both live under attempt-1/supervisor-try-N.
	tryOneErr := filepath.Join(attemptDir, "supervisor-try-1", "supervisor_error.json")
	data, err := os.ReadFile(tryOneErr)
	if err != nil {
		t.Fatalf("supervisor-try-1/supervisor_error.json missing: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("decode supervisor_error.json: %v", err)
	}
	if payload["kind"] != "idle_timeout" {
		t.Fatalf("supervisor-try-1 kind got %v, want idle_timeout (real idle-timeout path)", payload["kind"])
	}
	if _, err := os.Stat(filepath.Join(attemptDir, "supervisor-try-2", "supervisor_verdict.json")); err != nil {
		t.Fatalf("supervisor-try-2/supervisor_verdict.json missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(attemptDir, "supervisor-try-3")); !os.IsNotExist(err) {
		t.Fatalf("supervisor-try-3 should not exist after recovery on try 2, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(attemptDir, "model_supervisor_verdict.json")); err != nil {
		t.Fatalf("top-level model_supervisor_verdict.json missing after recovery: %v", err)
	}
}

// TestDaemonSupervisorStallExhaustsRetryBudget exercises AC-002 end to end:
// when the built-in supervisor keeps stalling, the daemon exhausts the fixed
// retry budget inside attempt-1, marks the task needs_supervisor_review, moves
// it to failed/, persists the attempt error phase/kind/artifact dir, keeps
// every retry's error artifact under the original attempt, and never opens a
// new executor attempt.
func TestDaemonSupervisorStallExhaustsRetryBudget(t *testing.T) {
	root, taskPath, _ := newDaemonRetryTask(t)
	promptPath, schemaPath := writeDaemonPromptFiles(t)
	claudeBin := writeFakeClaude(t, "echo change > daemon-output.txt\necho '{\"status\":\"completed\",\"summary\":\"done\",\"files_modified\":[\"daemon-output.txt\"],\"acceptance_criteria\":[{\"id\":\"AC1\",\"status\":\"satisfied\",\"evidence\":[\"diff\"],\"notes\":\"done\"}],\"verification\":[],\"decisions\":[],\"risks\":[]}'\n")
	codexBin := writeAlwaysStallCodexSupervisor(t)
	setLoopBudget(t, taskPath, 3)

	err := Run(context.Background(), Options{
		Root:               root,
		SystemPromptFile:   promptPath,
		JSONSchemaFile:     schemaPath,
		Once:               true,
		MaxConcurrentTasks: 1,
		Supervisor:         "codex",
		ClaudeBin:          claudeBin,
		CodexBin:           codexBin,
		IdleTimeout:        200 * time.Millisecond,
	})
	if err == nil {
		t.Fatal("expected the daemon run to surface the exhausted supervisor stall")
	}

	failedTask, err := task.Load(filepath.Join(root, "tasks", "failed", "task.yaml"))
	if err != nil {
		t.Fatalf("failed task missing: %v", err)
	}
	if failedTask.Status != "needs_supervisor_review" {
		t.Fatalf("status got %q, want needs_supervisor_review", failedTask.Status)
	}
	if len(failedTask.Attempts) != 1 {
		t.Fatalf("attempts got %d, want 1", len(failedTask.Attempts))
	}
	last := failedTask.Attempts[0]
	if last.Error == nil {
		t.Fatalf("attempt error missing: %#v", last)
	}
	if last.Error.Phase != "supervisor" {
		t.Fatalf("attempt error phase got %q, want supervisor", last.Error.Phase)
	}
	if last.Error.Kind != "idle_timeout" {
		t.Fatalf("attempt error kind got %q, want idle_timeout", last.Error.Kind)
	}
	if last.Error.ArtifactDir == "" {
		t.Fatalf("attempt error artifact dir not persisted: %#v", last.Error)
	}
	if !strings.Contains(last.Error.ArtifactDir, "attempt-1") {
		t.Fatalf("attempt error artifact dir %q must point at attempt-1", last.Error.ArtifactDir)
	}

	matches, err := filepath.Glob(filepath.Join(root, "runs", "*", "attempt-1"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("attempt-1 dir glob got %v (err=%v), want exactly one", matches, err)
	}
	attemptDir := matches[0]
	assertGlobCount(t, filepath.Join(root, "runs", "*", "attempt-2"), 0)
	// All retry error artifacts remain under the original attempt: the
	// initial try plus the full retry budget, each with its own error JSON.
	for i := 1; i <= supervisorTotalAttempts; i++ {
		p := filepath.Join(attemptDir, fmt.Sprintf("supervisor-try-%d", i), "supervisor_error.json")
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("supervisor-try-%d/supervisor_error.json missing under attempt-1: %v", i, err)
		}
	}
	if _, err := os.Stat(filepath.Join(attemptDir, fmt.Sprintf("supervisor-try-%d", supervisorTotalAttempts+1))); !os.IsNotExist(err) {
		t.Fatalf("there must be no supervisor-try beyond the budget, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(attemptDir, "model_supervisor_verdict.json")); !os.IsNotExist(err) {
		t.Fatalf("model_supervisor_verdict.json must not be written after exhaustion, stat err=%v", err)
	}
}
