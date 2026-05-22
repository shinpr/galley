package daemon

// AC: AC5, AC6 — End-to-end daemon coverage for the acceptance skeleton
// creator provider routing.
//
// Behavior under test:
//   - Trigger: daemon Run processes a preflight-enabled task whose
//     executor.cli is "codex" while the daemon supervisor backend is Claude.
//   - Process: AcceptanceSkeletonPreflight routes the creator through the
//     Codex command planner (following task.executor.cli, not the supervisor
//     backend), captures the manifest from the `codex exec
//     --output-last-message` file, updates the running task, and the Codex
//     executor attempt then completes against the generated skeleton.
//   - Observable result: the task reaches tasks/done with generated
//     preflight outputs and AC verification annotations; the persisted run
//     evidence (preflight_creator_command_plan.json,
//     preflight_creator_manifest.json, preflight_result.json) shows the Codex
//     provider was used even though the supervisor backend is Claude.
//
// @lane: integration
// @category: persistence
// @dependency: daemon Run, AcceptanceSkeletonPreflight, runner Codex planner,
//   fake codex creator+executor binary, fake claude supervisor
// @complexity: high

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shinpr/galley/internal/task"
)

// TestRunOnceAcceptanceSkeletonCreatorRoutesCodexProvider proves AC5/AC6: a
// codex-executor task runs the skeleton creator through `codex exec` even
// though the daemon supervisor backend is Claude, and the manifest captured
// from the Codex last-message file produces the same persisted preflight
// evidence as the Claude creator path.
func TestRunOnceAcceptanceSkeletonCreatorRoutesCodexProvider(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	promptPath, schemaPath := writeDaemonPromptFiles(t)

	creatorManifest := `{"outputs":[{"ac_id":"AC1","path":"internal/foo/foo_test.go","kind":"go-test","purpose":"verify AC1","satisfies":"AC1 observable behavior","integration_point":"executor completes this skeleton before acceptance","implementation_required":true}],"no_skeletons":[]}`
	executorResult := `{"status":"completed","summary":"codex done","files_modified":["daemon-output.txt","internal/foo/foo_test.go"],"acceptance_criteria":[{"id":"AC1","status":"satisfied","evidence":["diff"],"notes":"done"}],"verification":[],"decisions":[],"risks":[]}`

	// One fake `codex` binary serves both the acceptance skeleton creator and
	// the implementation executor. It branches on the combined stdin prompt:
	// the creator prompt contains the phrase "acceptance skeleton creator",
	// the executor prompt does not. Both paths write their structured result
	// to the `--output-last-message` capture file the way the real Codex CLI
	// writes its final assistant message (R2).
	codexBin := writeFakeCommand(t, "codex", `out=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --output-last-message)
      out="$2"
      shift 2
      ;;
    *)
      shift
      ;;
  esac
done
input="$(cat)"
case "$input" in
  *"acceptance skeleton creator"*)
    mkdir -p internal/foo
    printf 'package foo_test\n\n// TODO(galley-skeleton): implement AC1 assertion.\n' > internal/foo/foo_test.go
    printf '%s\n' '`+creatorManifest+`' > "$out"
    ;;
  *)
    echo change > daemon-output.txt
    printf '%s\n' '`+executorResult+`' > "$out"
    ;;
esac
`)
	// Claude is used only as the daemon supervisor. Its supervisor branch
	// exits before the body, so the body never runs (the codex task never
	// invokes claude as the executor).
	claudeBin := writeFakeClaude(t, "exit 1\n")

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
	loaded.Executor.Model = "gpt-5-codex"
	loaded.Preflight = &task.Preflight{AcceptanceSkeleton: &task.AcceptanceSkeletonConfig{Enabled: true}}
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

	doneTask, err := task.Load(filepath.Join(root, "tasks", "done", "task.yaml"))
	if err != nil {
		t.Fatalf("task did not reach done: %v", err)
	}
	outputs := doneTask.Preflight.AcceptanceSkeleton.Outputs
	if len(outputs) != 1 || outputs[0].Path != "internal/foo/foo_test.go" || outputs[0].Satisfies == "" || outputs[0].IntegrationPoint == "" {
		t.Fatalf("generated outputs not written to task: %+v", outputs)
	}
	if !strings.Contains(doneTask.AcceptanceCriteria[0].Verification, "Acceptance skeleton:") ||
		!strings.Contains(doneTask.AcceptanceCriteria[0].Verification, "AC1 observable behavior") {
		t.Fatalf("AC verification not annotated:\n%s", doneTask.AcceptanceCriteria[0].Verification)
	}

	assertGlobCount(t, filepath.Join(root, "runs", "*", "preflight_creator_command_plan.json"), 1)
	assertGlobCount(t, filepath.Join(root, "runs", "*", "preflight_creator_manifest.json"), 1)
	assertGlobCount(t, filepath.Join(root, "runs", "*", "preflight_result.json"), 1)

	// The creator followed task.executor.cli (Codex), not the daemon
	// supervisor backend (Claude).
	matches, err := filepath.Glob(filepath.Join(root, "runs", "*", "preflight_creator_command_plan.json"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("preflight_creator_command_plan.json glob = %v (err %v)", matches, err)
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("read creator command plan: %v", err)
	}
	var plan struct {
		Argv []string `json:"argv"`
	}
	if err := json.Unmarshal(data, &plan); err != nil {
		t.Fatalf("decode creator command plan: %v", err)
	}
	if len(plan.Argv) < 2 || filepath.Base(plan.Argv[0]) != "codex" || plan.Argv[1] != "exec" {
		t.Fatalf("creator did not follow codex executor backend: %+v", plan.Argv)
	}
}
