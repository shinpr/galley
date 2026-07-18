package daemon

// AC: AC2 — Daemon execution dispatches implementation attempts through the
// executor selected by task.executor.cli, invoking Claude for "claude" and
// Codex for "codex".
//
// Behavior under test:
//   - Trigger: daemon Run processes a queued task whose executor.cli is set
//     to one of {"claude", "codex"}.
//   - Process: runExecutorAttempt (internal/daemon/loop.go) selects the
//     executor adapter that matches task.executor.cli and constructs a
//     command plan that points at the corresponding binary.
//   - Observable result: when cli="claude" the fake claude binary is invoked
//     and produces an executor_result.json under runs/<id>/attempt-1/; when
//     cli="codex" the fake codex executor binary is invoked instead. The
//     non-selected binary records zero invocations. The task moves to the
//     expected terminal state for the supervisor verdict path already used by
//     the Claude flow.
//
// @lane: integration
// @category: core-functionality
// @dependency: daemon Run, runner adapters, supervisor fake
// @complexity: medium
// @roi: business_value=10 * user_frequency=10 + legal=0 + defect=10 -> 110
// @timing: alongside implementation (daemon dispatch wiring)
// @placement: existing daemon_test.go covers Claude paths; this skeleton
// groups Codex dispatch parity around its own table to keep diff review
// scoped (see review_dimensions.reviewable-diff-shape).

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shinpr/galley/internal/task"
)

// writeFakeCodexExecutor returns the path to a fake `codex` binary that acts
// as the Codex executor for AC2/AC4 daemon tests. The fake binary reads the
// stdin prompt (the daemon delivers the combined system + work-order prompt
// through stdin per CodexCommandPlan) and then runs the caller-supplied body
// to record invocation markers and emit the executor result JSON line.
func writeFakeCodexExecutor(t *testing.T, body string) string {
	t.Helper()
	// Emit a realistic Codex `turn.completed` terminal after the body so the
	// normal-terminal decision matches production routing; interruption bodies
	// exit before this line or emit their own terminal event.
	return writeFakeCommand(t, "codex", "cat >/dev/null\n"+body+`
printf '%s\n' '{"type":"turn.completed","usage":{}}'
`)
}

// TestDaemonDispatchesSelectedExecutorBinary parameterizes over the supported
// executor.cli values and verifies the right binary is invoked.
func TestDaemonDispatchesSelectedExecutorBinary(t *testing.T) {
	cases := []struct {
		name string
		cli  string
	}{
		{"claude", "claude"},
		{"codex", "codex"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), ".agent-workflow")
			repo := initDaemonGitRepo(t)
			promptPath, schemaPath := writeDaemonPromptFiles(t)

			markerDir := t.TempDir()
			claudeExecMarker := filepath.Join(markerDir, "claude-exec.marker")
			codexExecMarker := filepath.Join(markerDir, "codex-exec.marker")

			executorResult := `{"status":"completed","summary":"done","files_modified":["daemon-output.txt"],"acceptance_criteria":[{"id":"AC1","status":"satisfied","evidence":["diff"],"notes":"done"}],"verification":[],"scope_expansions":[],"decisions":[],"risks":[]}`

			// The supervisor branch in writeFakeClaude exits before running
			// body, so the executor marker only fires when claude is invoked
			// as the executor (not as the supervisor on a codex task).
			claudeBin := writeFakeClaude(t, "echo executor >> "+claudeExecMarker+"\necho change > daemon-output.txt\necho '"+executorResult+"'\n")
			codexBin := writeFakeCodexExecutor(t, "echo executor >> "+codexExecMarker+"\necho change > daemon-output.txt\necho '"+executorResult+"'\n")

			taskPath := filepath.Join(root, "tasks", "queued", "task.yaml")
			if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
				t.Fatal(err)
			}
			writeDaemonTask(t, taskPath, repo)
			loaded, err := task.Load(taskPath)
			if err != nil {
				t.Fatal(err)
			}
			loaded.Executor.CLI = tc.cli
			if err := task.Save(taskPath, loaded); err != nil {
				t.Fatal(err)
			}

			err = runTestDaemon(context.Background(), Options{
				Root:               root,
				SystemPromptFile:   promptPath,
				JSONSchemaFile:     schemaPath,
				Once:               true,
				MaxConcurrentTasks: 1,
				Supervisor:         "claude",
				ClaudeBin:          claudeBin,
				CodexBin:           codexBin,
			})
			if err != nil {
				t.Fatal(err)
			}

			doneTask, err := task.Load(filepath.Join(root, "tasks", "done", "task.yaml"))
			if err != nil {
				t.Fatalf("done task missing: %v", err)
			}
			if doneTask.Status != "accepted" {
				t.Fatalf("status got %q", doneTask.Status)
			}
			if len(doneTask.Attempts) != 1 || doneTask.Attempts[0].ClaudeStatus != "completed" {
				t.Fatalf("attempts got %#v", doneTask.Attempts)
			}

			if tc.cli == "claude" {
				if _, err := os.Stat(claudeExecMarker); err != nil {
					t.Fatalf("claude executor marker missing: %v", err)
				}
				if _, err := os.Stat(codexExecMarker); !os.IsNotExist(err) {
					t.Fatalf("codex executor marker should not exist for cli=claude: %v", err)
				}
			} else {
				if _, err := os.Stat(codexExecMarker); err != nil {
					t.Fatalf("codex executor marker missing: %v", err)
				}
				if _, err := os.Stat(claudeExecMarker); !os.IsNotExist(err) {
					// claudeBin was invoked as supervisor only, which exits
					// before running the body that writes the marker.
					t.Fatalf("claude executor marker should not exist for cli=codex (supervisor path only): %v", err)
				}
			}

			planData := mustReadSingleGlob(t, filepath.Join(root, "runs", "*", "attempt-1", "command_plan.json"))
			var plan struct {
				Argv []string `json:"argv"`
				Env  []string `json:"env,omitempty"`
			}
			if err := json.Unmarshal(planData, &plan); err != nil {
				t.Fatalf("decode command_plan.json: %v", err)
			}
			if len(plan.Argv) == 0 {
				t.Fatal("command_plan.json argv is empty")
			}
			if len(plan.Env) != 0 {
				t.Fatalf("command_plan.json must not persist environment entries: %v", plan.Env)
			}
			wantBin := claudeBin
			if tc.cli == "codex" {
				wantBin = codexBin
			}
			if filepath.Base(plan.Argv[0]) != filepath.Base(wantBin) {
				t.Fatalf("command_plan.json argv[0] basename got %q, want %q (selected cli=%s)", filepath.Base(plan.Argv[0]), filepath.Base(wantBin), tc.cli)
			}
		})
	}
}

// TestDaemonRejectsUnknownExecutorCLIAtRun verifies the AC2 negative path so
// dispatch never silently falls back to Claude when an unrecognized CLI
// reaches the daemon. The task YAML is rewritten in place with
// executor.cli="opus-cli" so the value reaches the daemon's task loader (and
// the dispatch switch in runExecutorAttempt) without being short-circuited by
// a higher-level guard outside the daemon.
func TestDaemonRejectsUnknownExecutorCLIAtRun(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	promptPath, schemaPath := writeDaemonPromptFiles(t)

	markerDir := t.TempDir()
	claudeExecMarker := filepath.Join(markerDir, "claude-exec.marker")
	codexExecMarker := filepath.Join(markerDir, "codex-exec.marker")

	claudeBin := writeFakeClaude(t, "echo executor >> "+claudeExecMarker+"\necho '{}'\n")
	codexBin := writeFakeCodexExecutor(t, "echo executor >> "+codexExecMarker+"\necho '{}'\n")

	taskPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, taskPath, repo)
	raw, err := os.ReadFile(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	patched := strings.Replace(string(raw), `cli: "claude"`, `cli: "opus-cli"`, 1)
	if patched == string(raw) {
		t.Fatal("could not patch executor.cli in task YAML; daemon test scaffold drifted")
	}
	if err := os.WriteFile(taskPath, []byte(patched), 0o600); err != nil {
		t.Fatal(err)
	}

	err = runTestDaemon(context.Background(), Options{
		Root:               root,
		SystemPromptFile:   promptPath,
		JSONSchemaFile:     schemaPath,
		Once:               true,
		MaxConcurrentTasks: 1,
		Supervisor:         "claude",
		ClaudeBin:          claudeBin,
		CodexBin:           codexBin,
	})
	if err == nil {
		t.Fatal("expected daemon Run to surface the unknown executor.cli error")
	}

	failedTask, err := task.Load(filepath.Join(root, "tasks", "failed", "task.yaml"))
	if err != nil {
		t.Fatalf("failed task missing: %v", err)
	}
	if len(failedTask.Attempts) == 0 {
		t.Fatal("expected at least one recorded attempt for the unknown-CLI task")
	}
	last := failedTask.Attempts[len(failedTask.Attempts)-1]
	if last.Error == nil {
		t.Fatalf("attempt error missing: %#v", last)
	}
	if !strings.Contains(last.Error.Message, "executor.cli") {
		t.Fatalf("attempt error message should name executor.cli, got %q", last.Error.Message)
	}

	if _, err := os.Stat(claudeExecMarker); !os.IsNotExist(err) {
		t.Fatalf("claude executor marker should not exist on rejected dispatch: %v", err)
	}
	if _, err := os.Stat(codexExecMarker); !os.IsNotExist(err) {
		t.Fatalf("codex executor marker should not exist on rejected dispatch: %v", err)
	}
}
