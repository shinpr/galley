package daemon

// AC: AC4 — Codex executor result capture must reach supervisor handoff with
// the same fidelity the Claude path provides. Specifically:
//   - command_plan.json must include real --output-schema and
//     --output-last-message paths so the upstream `codex exec` CLI can write
//     its structured result alongside the rest of the attempt evidence.
//   - When the fake Codex executor writes a completed final-message file,
//     executor_result.json must contain the executor's reported status and
//     summary (parsed from the last-message file, not synthesized).
//   - When the executor reports hard_stop, the daemon must preserve that
//     judgment instead of overlaying fallback generated evidence — losing
//     the hard_stop reason would erase the unblock instructions the
//     supervisor needs.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shinpr/galley/internal/runartifact"
	"github.com/shinpr/galley/internal/runner"
	"github.com/shinpr/galley/internal/task"
)

// writeFakeCodexCapturingExecutor returns a fake `codex` binary that reads the
// argv for --output-last-message and --output-schema, writes the supplied
// executor result body to the last-message capture path, and echoes the same
// body on stdout so JSONL-only parsing also has data to fall back to. The
// schemaMarker, when non-empty, is written next to the resolved schema file so
// tests can assert which `codex exec --output-schema <file>` path was passed.
func writeFakeCodexCapturingExecutor(t *testing.T, executorResult, schemaMarker, sideEffect string) string {
	t.Helper()
	body := `lastmsg=""
schema=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --output-last-message)
      lastmsg="$2"
      shift 2
      ;;
    --output-schema)
      schema="$2"
      shift 2
      ;;
    *)
      shift
      ;;
  esac
done
cat >/dev/null
` + sideEffect + `
if [ -n "$lastmsg" ]; then
  printf '%s' '` + executorResult + `' > "$lastmsg"
fi
if [ -n "$schema" ] && [ -n "` + schemaMarker + `" ]; then
  printf '%s' "$schema" > "` + schemaMarker + `"
fi
printf '%s\n' '` + executorResult + `'
`
	return writeFakeCommand(t, "codex", body)
}

func TestCodexCommandPlanRecordsOutputSchemaAndLastMessage(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	promptPath, schemaPath := writeDaemonPromptFiles(t)

	executorResult := `{"status":"completed","summary":"codex done","files_modified":["daemon-output.txt"],"acceptance_criteria":[{"id":"AC1","status":"satisfied","evidence":["diff"],"notes":"done"}],"verification":[],"scope_expansions":[],"decisions":[],"risks":[]}`
	claudeBin := writeFakeClaude(t, "exit 1\n")
	codexBin := writeFakeCodexCapturingExecutor(t, executorResult, "", "echo change > daemon-output.txt\n")

	taskPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, taskPath, repo)
	loaded, err := task.Load(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Executor.CLI = "codex"
	if err := task.Save(taskPath, loaded); err != nil {
		t.Fatal(err)
	}

	if err := Run(context.Background(), Options{
		Root:               root,
		SystemPromptFile:   promptPath,
		JSONSchemaFile:     schemaPath,
		Once:               true,
		MaxConcurrentTasks: 1,
		Supervisor:         "claude",
		ClaudeBin:          claudeBin,
		CodexBin:           codexBin,
	}); err != nil {
		t.Fatal(err)
	}

	planData := mustReadSingleGlob(t, filepath.Join(root, "runs", "*", "attempt-1", "command_plan.json"))
	var plan struct {
		Argv []string `json:"argv"`
	}
	if err := json.Unmarshal(planData, &plan); err != nil {
		t.Fatalf("decode command_plan.json: %v", err)
	}

	gotSchema := flagFromArgv(plan.Argv, "--output-schema")
	if gotSchema == "" {
		t.Fatalf("command_plan.json argv missing --output-schema: %v", plan.Argv)
	}
	// When the daemon Options carry a JSONSchemaFile path, the Codex runner
	// must reuse that real file rather than materializing a new one.
	if gotSchema != schemaPath {
		t.Fatalf("output-schema not pointed at supplied schema: got %q, want %q", gotSchema, schemaPath)
	}
	gotLast := flagFromArgv(plan.Argv, "--output-last-message")
	if gotLast == "" {
		t.Fatalf("command_plan.json argv missing --output-last-message: %v", plan.Argv)
	}
}

func TestCodexCommandPlanMaterializesEmbeddedSchemaWhenNoFileProvided(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	promptPath, _ := writeDaemonPromptFiles(t)

	executorResult := `{"status":"completed","summary":"codex done","files_modified":["daemon-output.txt"],"acceptance_criteria":[{"id":"AC1","status":"satisfied","evidence":["diff"],"notes":"done"}],"verification":[],"scope_expansions":[],"decisions":[],"risks":[]}`
	claudeBin := writeFakeClaude(t, "exit 1\n")
	codexBin := writeFakeCodexCapturingExecutor(t, executorResult, "", "echo change > daemon-output.txt\n")

	taskPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, taskPath, repo)
	loaded, err := task.Load(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Executor.CLI = "codex"
	if err := task.Save(taskPath, loaded); err != nil {
		t.Fatal(err)
	}

	// Intentionally omit JSONSchemaFile so the runner has to materialize the
	// embedded schema into an attempt-scoped file.
	if err := Run(context.Background(), Options{
		Root:               root,
		SystemPromptFile:   promptPath,
		Once:               true,
		MaxConcurrentTasks: 1,
		Supervisor:         "claude",
		ClaudeBin:          claudeBin,
		CodexBin:           codexBin,
	}); err != nil {
		t.Fatal(err)
	}

	planData := mustReadSingleGlob(t, filepath.Join(root, "runs", "*", "attempt-1", "command_plan.json"))
	var plan struct {
		Argv []string `json:"argv"`
	}
	if err := json.Unmarshal(planData, &plan); err != nil {
		t.Fatalf("decode command_plan.json: %v", err)
	}
	gotSchema := flagFromArgv(plan.Argv, "--output-schema")
	if gotSchema == "" {
		t.Fatalf("command_plan.json argv missing --output-schema: %v", plan.Argv)
	}
	if _, err := os.Stat(gotSchema); err != nil {
		t.Fatalf("attempt-scoped schema file must exist on disk: %v", err)
	}
}

func TestCodexExecutorLastMessageJSONReachesClaudeResult(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	promptPath, schemaPath := writeDaemonPromptFiles(t)

	executorResult := `{"status":"completed","summary":"codex last-message summary","files_modified":["daemon-output.txt"],"acceptance_criteria":[{"id":"AC1","status":"satisfied","evidence":["diff"],"notes":"done"}],"verification":[],"scope_expansions":[],"decisions":[],"risks":[]}`
	// The fake executor writes the result to --output-last-message and emits a
	// non-result line on stdout so the test only passes when the daemon parses
	// the last-message file rather than relying on stdout JSONL.
	claudeBin := writeFakeClaude(t, "exit 1\n")
	codexBin := writeFakeCommand(t, "codex", `lastmsg=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --output-last-message)
      lastmsg="$2"
      shift 2
      ;;
    *)
      shift
      ;;
  esac
done
cat >/dev/null
echo change > daemon-output.txt
if [ -n "$lastmsg" ]; then
  printf '%s' '`+executorResult+`' > "$lastmsg"
fi
printf '%s\n' '{"event":"unrelated"}'
`)

	taskPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, taskPath, repo)
	loaded, err := task.Load(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Executor.CLI = "codex"
	if err := task.Save(taskPath, loaded); err != nil {
		t.Fatal(err)
	}

	if err := Run(context.Background(), Options{
		Root:               root,
		SystemPromptFile:   promptPath,
		JSONSchemaFile:     schemaPath,
		Once:               true,
		MaxConcurrentTasks: 1,
		Supervisor:         "claude",
		ClaudeBin:          claudeBin,
		CodexBin:           codexBin,
	}); err != nil {
		t.Fatal(err)
	}

	data := mustReadSingleGlob(t, filepath.Join(root, "runs", "*", "attempt-1", runartifact.ExecutorResultFilename))
	var got runner.ClaudeResult
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decode executor_result.json: %v", err)
	}
	if got.Status != "completed" {
		t.Fatalf("status got %q, want completed (parsed from last-message file)", got.Status)
	}
	if !strings.Contains(got.Summary, "codex last-message summary") {
		t.Fatalf("summary missing last-message body; got %q", got.Summary)
	}
}

func TestCodexExecutorHardStopFromLastMessageIsPreserved(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	promptPath, schemaPath := writeDaemonPromptFiles(t)

	hardStopResult := `{"status":"hard_stop","summary":"codex hard stop","files_modified":[],"acceptance_criteria":[],"verification":[],"scope_expansions":[],"decisions":[],"risks":[],"hard_stop":{"reason":"required secret missing","attempted":["checked env"],"needed_to_continue":["set FOO_TOKEN"]}}`
	claudeBin := writeFakeClaude(t, "exit 1\n")
	codexBin := writeFakeCommand(t, "codex", `lastmsg=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --output-last-message)
      lastmsg="$2"
      shift 2
      ;;
    *)
      shift
      ;;
  esac
done
cat >/dev/null
if [ -n "$lastmsg" ]; then
  printf '%s' '`+hardStopResult+`' > "$lastmsg"
fi
printf '%s\n' '`+hardStopResult+`'
`)

	taskPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, taskPath, repo)
	loaded, err := task.Load(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Executor.CLI = "codex"
	if err := task.Save(taskPath, loaded); err != nil {
		t.Fatal(err)
	}

	if err := Run(context.Background(), Options{
		Root:               root,
		SystemPromptFile:   promptPath,
		JSONSchemaFile:     schemaPath,
		Once:               true,
		MaxConcurrentTasks: 1,
		Supervisor:         "claude",
		ClaudeBin:          claudeBin,
		CodexBin:           codexBin,
	}); err != nil {
		t.Fatal(err)
	}

	data := mustReadSingleGlob(t, filepath.Join(root, "runs", "*", "attempt-1", runartifact.ExecutorResultFilename))
	var got runner.ClaudeResult
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decode executor_result.json: %v", err)
	}
	if got.Status != "hard_stop" {
		t.Fatalf("Codex hard_stop must survive resolveExecutorResult: got status %q", got.Status)
	}
	if got.HardStop == nil {
		t.Fatalf("hard_stop details missing: %#v", got)
	}
	if got.HardStop.Reason != "required secret missing" {
		t.Fatalf("hard_stop reason was overwritten by fallback generated evidence: got %q", got.HardStop.Reason)
	}
	if len(got.HardStop.NeededToContinue) == 0 || got.HardStop.NeededToContinue[0] != "set FOO_TOKEN" {
		t.Fatalf("hard_stop needed_to_continue was overwritten: %#v", got.HardStop.NeededToContinue)
	}
}

func flagFromArgv(argv []string, name string) string {
	for i := 0; i < len(argv)-1; i++ {
		if argv[i] == name {
			return argv[i+1]
		}
	}
	return ""
}
