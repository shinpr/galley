package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shinpr/galley/internal/task"
)

// TestRunOnceDeliversSkeletonObligationsToExecutor proves the runtime skeleton
// obligations reach the executor process, not merely the string builder.
// skeletonpreflight.AppendObligations has a pure unit test on its output, but
// runSupervisorLoop is the only place that loads preflight_result.json, appends
// the obligations to the rendered work order, and hands the combined prompt to
// the executor command plan. On macOS/Linux the Claude executor receives the
// work order as its final argv element, so a fake executor that dumps its argv
// captures exactly what the real executor would see. A regression that stopped
// appending obligations, or appended them to the wrong prompt, would leave the
// AppendObligations unit test green while the executor never received the
// AC-to-skeleton bindings. This test asserts the dumped executor input contains
// the runtime obligations block and the AC1 -> internal/foo/foo_test.go mapping.
func TestRunOnceDeliversSkeletonObligationsToExecutor(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	promptPath, schemaPath := writeDaemonPromptFiles(t)
	dump := filepath.Join(t.TempDir(), "executor-input.txt")
	// creator branch: materialize the skeleton and emit its manifest so preflight
	// writes preflight_result.json. executor branch: dump the full argv (which
	// carries the work order prompt plus appended obligations) and make a real
	// change so the run reaches the supervisor.
	claudeBin := writeFakeClaude(t, `creator=0
for arg in "$@"; do
  case "$arg" in
    *"Galley Acceptance Skeleton Manifest"*) creator=1 ;;
  esac
done
if [ "$creator" = "1" ]; then
  mkdir -p internal/foo
  printf 'package foo_test\n\n// TODO(galley-skeleton): implement AC1 assertion.\n' > internal/foo/foo_test.go
  printf '%s\n' '{"type":"result","result":"{\"outputs\":[{\"ac_id\":\"AC1\",\"path\":\"internal/foo/foo_test.go\",\"kind\":\"go-test\",\"purpose\":\"verify AC1\",\"satisfies\":\"AC1 observable behavior\",\"integration_point\":\"executor completes this skeleton before acceptance\",\"implementation_required\":true}],\"no_skeletons\":[]}"}'
  exit 0
fi
printf '%s\n' "$@" > `+shellPath(dump)+`
echo change > daemon-output.txt
echo '{"status":"completed","summary":"done","files_modified":["daemon-output.txt","internal/foo/foo_test.go"],"acceptance_criteria":[{"id":"AC1","status":"satisfied","evidence":["diff"],"notes":"done"}],"verification":[],"scope_expansions":[],"decisions":[],"risks":[]}'
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
	loaded.Preflight = &task.Preflight{AcceptanceSkeleton: &task.AcceptanceSkeletonConfig{Enabled: true}}
	if err := task.Save(taskPath, loaded); err != nil {
		t.Fatal(err)
	}

	// The task may finalize (accepted) or be downgraded by the acceptance gate;
	// either way the executor runs first and dumps its input. The final status is
	// not the subject under test.
	if err := Run(context.Background(), Options{
		Root:               root,
		SystemPromptFile:   promptPath,
		JSONSchemaFile:     schemaPath,
		Once:               true,
		MaxConcurrentTasks: 1,
		Supervisor:         "claude",
		ClaudeBin:          claudeBin,
	}); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(dump)
	if err != nil {
		t.Fatalf("executor was never invoked with a dumped work order: %v", err)
	}
	input := string(data)
	if !strings.Contains(input, "Acceptance Skeleton Obligations (Runtime)") {
		t.Fatalf("executor input missing runtime obligations block:\n%s", input)
	}
	if !strings.Contains(input, "AC `AC1` -> `internal/foo/foo_test.go`") {
		t.Fatalf("executor input missing AC-to-skeleton mapping:\n%s", input)
	}
}
