package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shinpr/galley/internal/task"
)

// A standing preflight skeleton diff must not hide repeated no-progress attempts.
func TestRunOnceSkeletonOnlyAttemptsFailViaNoDiffInvariant(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	promptPath, schemaPath := writeDaemonPromptFiles(t)
	// The creator branch (detected by the skeleton manifest prompt) materializes
	// internal/foo/foo_test.go and returns its manifest. The executor branch
	// re-writes byte-identical skeleton content (so the baseline hash matches and
	// no non-skeleton progress is recorded) and reports completed_with_risks so
	// the fake supervisor returns needs_revision and the loop retries.
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
mkdir -p internal/foo
printf 'package foo_test\n\n// TODO(galley-skeleton): implement AC1 assertion.\n' > internal/foo/foo_test.go
echo '{"status":"completed_with_risks","summary":"only reproduced the skeleton","files_modified":["internal/foo/foo_test.go"],"acceptance_criteria":[{"id":"AC1","status":"satisfied","evidence":["skeleton"],"notes":"skeleton only"}],"verification":[],"scope_expansions":[],"decisions":[],"risks":[{"type":"partial_verification","detail":"only the skeleton was produced","mitigation":"implement the behavior the skeleton verifies","needs_human_review":true}]}'
`)
	taskPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, taskPath, repo)
	setLoopBudget(t, taskPath, 5)
	loaded, err := task.Load(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Preflight = &task.Preflight{AcceptanceSkeleton: &task.AcceptanceSkeletonConfig{Enabled: true}}
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
	})
	if err != nil {
		t.Fatal(err)
	}

	failedTask, err := task.Load(filepath.Join(root, "tasks", "failed", "task.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if failedTask.Status != "failed" {
		t.Fatalf("status got %q, want failed", failedTask.Status)
	}
	// The escalation must be the consecutive no-diff progress invariant, not any
	// other risk: the standing skeleton diff must have been discounted.
	if len(failedTask.Risks) == 0 || !strings.Contains(failedTask.Risks[len(failedTask.Risks)-1].Detail, "no git diff") {
		t.Fatalf("no-diff progress risk missing: %#v", failedTask.Risks)
	}
	// The escalation must take exactly the threshold number of attempts; a single
	// attempt would mean the loop stopped for a different reason.
	if len(failedTask.Attempts) != progressNoDiffThreshold {
		t.Fatalf("attempts got %d, want %d (consecutive no-diff threshold)", len(failedTask.Attempts), progressNoDiffThreshold)
	}
	// The skeleton file must still exist in the worktree: this is what made the
	// diff dirty on every attempt, so the escalation proves the baseline excluded
	// it rather than there simply being no diff at all.
	worktreePath := taskWorktreePath(repo, failedTask.Worktree.Path)
	if _, err := os.Stat(filepath.Join(worktreePath, "internal", "foo", "foo_test.go")); err != nil {
		t.Fatalf("preflight skeleton missing from worktree: %v", err)
	}
	assertGlobCount(t, filepath.Join(root, "runs", "*", "preflight_result.json"), 1)
}
