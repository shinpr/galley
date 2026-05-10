package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shinpr/galley/internal/task"
)

// preflightTestTask builds a minimal task with the acceptance skeleton stage
// enabled and a creator command. The creator command is shell run inside the
// prepared worktree; it always writes the manifest JSON to
// $GALLEY_SKELETON_MANIFEST and additionally runs createCmds beforehand.
func preflightTestTask(creatorCmd string, acIDs ...string) task.Task {
	if len(acIDs) == 0 {
		acIDs = []string{"AC1"}
	}
	crit := make([]task.AcceptanceCriterion, 0, len(acIDs))
	for _, id := range acIDs {
		crit = append(crit, task.AcceptanceCriterion{ID: id, Text: id, Verification: "see " + id, Status: "pending"})
	}
	return task.Task{
		ID:                 "preflight-test",
		AcceptanceCriteria: crit,
		Scope: task.Scope{
			AllowedPaths:   []string{"."},
			ForbiddenPaths: []string{"secret"},
		},
		Preflight: &task.Preflight{
			AcceptanceSkeleton: &task.AcceptanceSkeletonConfig{
				Enabled: true,
				Creator: &task.AcceptanceSkeletonCreatorDef{Command: creatorCmd},
			},
		},
	}
}

func runPreflight(t *testing.T, tk task.Task) (*AcceptanceSkeletonResult, error, string) {
	t.Helper()
	work := t.TempDir()
	runDir := t.TempDir()
	res, err := AcceptanceSkeletonPreflight(context.Background(), AcceptanceSkeletonPreflightOptions{
		Task:    tk,
		WorkDir: work,
		RunDir:  runDir,
	})
	return res, err, runDir
}

// manifestHeredoc returns a shell snippet that writes manifestJSON to
// $GALLEY_SKELETON_MANIFEST.
func manifestHeredoc(manifestJSON string) string {
	return "cat > \"$GALLEY_SKELETON_MANIFEST\" <<'GALLEYEOF'\n" + manifestJSON + "\nGALLEYEOF\n"
}

func TestAcceptanceSkeletonPreflightCreatorHappyPath(t *testing.T) {
	t.Parallel()
	manifest := `{"outputs":[{"ac_id":"AC1","path":"internal/foo/foo_test.go","kind":"go-test","purpose":"verify foo","implementation_required":true,"checkpoint_command":"true"}]}`
	cmd := "mkdir -p internal/foo && printf 'package foo\\n' > internal/foo/foo_test.go && " + manifestHeredoc(manifest)
	res, err, runDir := runPreflight(t, preflightTestTask(cmd))
	if err != nil {
		t.Fatalf("preflight error: %v", err)
	}
	if res == nil || res.Status != "completed" {
		t.Fatalf("status = %+v; want completed", res)
	}
	if len(res.Outputs) != 1 || res.Outputs[0].Path != "internal/foo/foo_test.go" || res.Outputs[0].ACID != "AC1" {
		t.Fatalf("outputs = %+v", res.Outputs)
	}
	if len(res.Baseline.SkeletonHashes) != 1 || res.Baseline.SkeletonHashes[0].Path != "internal/foo/foo_test.go" {
		t.Fatalf("baseline = %+v", res.Baseline)
	}
	if _, statErr := os.Stat(filepath.Join(runDir, "preflight_result.json")); statErr != nil {
		t.Fatalf("preflight_result.json not written: %v", statErr)
	}
}

func TestAcceptanceSkeletonPreflightCreatorMissingDeclaredFile(t *testing.T) {
	t.Parallel()
	// Creator declares an output but never writes it. Galley must fail rather
	// than auto-materialize a stub for a creator-declared output.
	manifest := `{"outputs":[{"ac_id":"AC1","path":"internal/foo/foo_test.go","kind":"go-test","purpose":"p","implementation_required":true,"checkpoint_command":"true"}]}`
	cmd := manifestHeredoc(manifest)
	res, err, _ := runPreflight(t, preflightTestTask(cmd))
	if err == nil {
		t.Fatalf("expected error for missing creator-declared file")
	}
	if res == nil || res.Status != "failed" || res.Error == nil {
		t.Fatalf("res = %+v", res)
	}
	if res.Error.Phase != "acceptance_skeleton_creator" || !strings.Contains(res.Error.Message, "does not exist") {
		t.Fatalf("error = %+v", res.Error)
	}
	// Confirm Galley did not create the file behind the creator's back.
	// (workDir is gone after runPreflight; assert via the failure path only.)
}

func TestAcceptanceSkeletonPreflightCreatorAbsolutePath(t *testing.T) {
	t.Parallel()
	manifest := `{"outputs":[{"ac_id":"AC1","path":"/etc/galley_skeleton_test.go","kind":"go-test","purpose":"p","implementation_required":true,"checkpoint_command":"true"}]}`
	cmd := manifestHeredoc(manifest)
	res, err, _ := runPreflight(t, preflightTestTask(cmd))
	if err == nil {
		t.Fatalf("expected error for absolute path")
	}
	if res == nil || res.Error == nil || res.Error.Phase != "acceptance_skeleton_provider" || !strings.Contains(res.Error.Message, "absolute") {
		t.Fatalf("error = %+v", res)
	}
}

func TestAcceptanceSkeletonPreflightCreatorParentTraversal(t *testing.T) {
	t.Parallel()
	manifest := `{"outputs":[{"ac_id":"AC1","path":"../escape_test.go","kind":"go-test","purpose":"p","implementation_required":true,"checkpoint_command":"true"}]}`
	cmd := manifestHeredoc(manifest)
	res, err, _ := runPreflight(t, preflightTestTask(cmd))
	if err == nil {
		t.Fatalf("expected error for parent traversal")
	}
	if res == nil || res.Error == nil || res.Error.Phase != "acceptance_skeleton_provider" || !strings.Contains(res.Error.Message, "traversal") {
		t.Fatalf("error = %+v", res)
	}
}

func TestAcceptanceSkeletonPreflightCreatorInvalidACID(t *testing.T) {
	t.Parallel()
	manifest := `{"outputs":[{"ac_id":"AC-nope","path":"foo_test.go","kind":"go-test","purpose":"p","implementation_required":true,"checkpoint_command":"true"}]}`
	cmd := "printf 'package foo\\n' > foo_test.go && " + manifestHeredoc(manifest)
	res, err, _ := runPreflight(t, preflightTestTask(cmd, "AC1"))
	if err == nil {
		t.Fatalf("expected error for invalid ac_id")
	}
	if res == nil || res.Error == nil || res.Error.Phase != "acceptance_skeleton_provider" || !strings.Contains(res.Error.Message, "acceptance_criteria.id") {
		t.Fatalf("error = %+v", res)
	}
}

func TestAcceptanceSkeletonPreflightCreatorForbiddenPath(t *testing.T) {
	t.Parallel()
	// "secret" is in scope.forbidden_paths; the manifest places an output there.
	manifest := `{"outputs":[{"ac_id":"AC1","path":"secret/foo_test.go","kind":"go-test","purpose":"p","implementation_required":true,"checkpoint_command":"true"}]}`
	cmd := "mkdir -p secret && printf 'package foo\\n' > secret/foo_test.go && " + manifestHeredoc(manifest)
	res, err, _ := runPreflight(t, preflightTestTask(cmd))
	if err == nil {
		t.Fatalf("expected error for forbidden path")
	}
	if res == nil || res.Error == nil || res.Error.Phase != "acceptance_skeleton_provider" || !strings.Contains(res.Error.Message, "forbidden_paths") {
		t.Fatalf("error = %+v", res)
	}
}

// assertPreflightCreatorRejected runs the preflight stage for a creator
// command, asserts it failed before the executor could be invoked (the daemon
// only enters runSupervisorLoop when AcceptanceSkeletonPreflight returns a nil
// error), and asserts the persisted preflight_result.json records a failed
// status with the acceptance_skeleton_provider phase and a message containing
// wantMsgFragment. It also confirms no skeleton baseline was captured, which
// would only happen after the (never-reached) hashing step.
func assertPreflightCreatorRejected(t *testing.T, cmd, wantMsgFragment string) {
	t.Helper()
	res, err, runDir := runPreflight(t, preflightTestTask(cmd))
	if err == nil {
		t.Fatalf("expected preflight to fail before executor invocation, got nil error (res=%+v)", res)
	}
	if res == nil || res.Status != "failed" || res.Error == nil {
		t.Fatalf("res = %+v; want failed status with error", res)
	}
	if res.Error.Phase != "acceptance_skeleton_provider" {
		t.Fatalf("error phase = %q; want acceptance_skeleton_provider (res=%+v)", res.Error.Phase, res)
	}
	if !strings.Contains(res.Error.Message, wantMsgFragment) {
		t.Fatalf("error message = %q; want fragment %q", res.Error.Message, wantMsgFragment)
	}
	if len(res.Outputs) != 0 || len(res.Baseline.SkeletonHashes) != 0 {
		t.Fatalf("rejected preflight should not record outputs/baseline: %+v", res)
	}
	persisted, lerr := LoadPreflightResult(runDir)
	if lerr != nil {
		t.Fatalf("LoadPreflightResult: %v", lerr)
	}
	if persisted == nil || persisted.Status != "failed" || persisted.Error == nil || persisted.Error.Phase != "acceptance_skeleton_provider" {
		t.Fatalf("persisted preflight_result.json = %+v; want failed provider record", persisted)
	}
}

func TestAcceptanceSkeletonPreflightCreatorEmptyCheckpointCommand(t *testing.T) {
	t.Parallel()
	// Even when the creator writes the skeleton file, an empty checkpoint
	// command must abort preflight: the daemon-side accepted gate cannot bind
	// evidence to an output with no command.
	manifest := `{"outputs":[{"ac_id":"AC1","path":"foo_test.go","kind":"go-test","purpose":"p","implementation_required":true,"checkpoint_command":"   "}]}`
	cmd := "printf 'package foo\\n' > foo_test.go && " + manifestHeredoc(manifest)
	assertPreflightCreatorRejected(t, cmd, "checkpoint_command")
}

func TestAcceptanceSkeletonPreflightCreatorMissingKind(t *testing.T) {
	t.Parallel()
	manifest := `{"outputs":[{"ac_id":"AC1","path":"foo_test.go","kind":"","purpose":"p","implementation_required":true,"checkpoint_command":"true"}]}`
	cmd := "printf 'package foo\\n' > foo_test.go && " + manifestHeredoc(manifest)
	assertPreflightCreatorRejected(t, cmd, "kind")
}

func TestAcceptanceSkeletonPreflightCreatorMissingPurpose(t *testing.T) {
	t.Parallel()
	manifest := `{"outputs":[{"ac_id":"AC1","path":"foo_test.go","kind":"go-test","purpose":"  ","implementation_required":true,"checkpoint_command":"true"}]}`
	cmd := "printf 'package foo\\n' > foo_test.go && " + manifestHeredoc(manifest)
	assertPreflightCreatorRejected(t, cmd, "purpose")
}

func TestAcceptanceSkeletonPreflightCreatorCheckpointAbsolutePathToken(t *testing.T) {
	t.Parallel()
	manifest := `{"outputs":[{"ac_id":"AC1","path":"foo_test.go","kind":"go-test","purpose":"p","implementation_required":true,"checkpoint_command":"/usr/bin/true"}]}`
	cmd := "printf 'package foo\\n' > foo_test.go && " + manifestHeredoc(manifest)
	assertPreflightCreatorRejected(t, cmd, "absolute path token")
}

func TestAcceptanceSkeletonPreflightCreatorCheckpointTraversalToken(t *testing.T) {
	t.Parallel()
	manifest := `{"outputs":[{"ac_id":"AC1","path":"foo_test.go","kind":"go-test","purpose":"p","implementation_required":true,"checkpoint_command":"go test ../other"}]}`
	cmd := "printf 'package foo\\n' > foo_test.go && " + manifestHeredoc(manifest)
	assertPreflightCreatorRejected(t, cmd, "parent-directory traversal token")
}

func TestAcceptanceSkeletonPreflightCreatorCheckpointDisallowedShellFeature(t *testing.T) {
	t.Parallel()
	manifest := `{"outputs":[{"ac_id":"AC1","path":"foo_test.go","kind":"go-test","purpose":"p","implementation_required":true,"checkpoint_command":"echo $(whoami)"}]}`
	cmd := "printf 'package foo\\n' > foo_test.go && " + manifestHeredoc(manifest)
	assertPreflightCreatorRejected(t, cmd, "disallowed shell feature")
}

func TestAcceptanceSkeletonPreflightCreatorCheckpointExternalRedirect(t *testing.T) {
	t.Parallel()
	manifest := `{"outputs":[{"ac_id":"AC1","path":"foo_test.go","kind":"go-test","purpose":"p","implementation_required":true,"checkpoint_command":"go test ./... 2>>../outside.log"}]}`
	cmd := "printf 'package foo\\n' > foo_test.go && " + manifestHeredoc(manifest)
	assertPreflightCreatorRejected(t, cmd, "redirect")
}
