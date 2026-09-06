package daemon

import (
	"os"
	"path/filepath"
	"testing"

	setuppreflight "github.com/shinpr/galley/internal/preflight/setup"
	skeletonpreflight "github.com/shinpr/galley/internal/preflight/skeleton"
	"github.com/shinpr/galley/internal/profile"
	"github.com/shinpr/galley/internal/task"
	"github.com/shinpr/galley/internal/workspace"
)

func TestSetupDoesNotReuseSuccessWithoutInputEvidence(t *testing.T) {
	root := t.TempDir()
	executor := task.Executor{CLI: "codex"}
	prior := filepath.Join(root, "runs", "freshness-1")
	result := &setuppreflight.Result{Status: "ready"}
	setuppreflight.ApplyExecutorIdentity(result, executor)
	if err := setuppreflight.WriteResult(prior, result); err != nil {
		t.Fatal(err)
	}
	current := filepath.Join(root, "runs", "freshness-2")
	if err := os.MkdirAll(current, 0o700); err != nil {
		t.Fatal(err)
	}
	_, reused, err := reuseReadySetup(preflightReuseQuery{Root: root, TaskID: "freshness", RunDir: current, Effective: executor, InputKey: "current-inputs"})
	if err != nil || reused {
		t.Fatalf("unverified prior inputs reused=%t: %v", reused, err)
	}
}

func TestSymlinkedSetupSignalDisablesReuse(t *testing.T) {
	workDir := t.TempDir()
	manifest := filepath.Join(t.TempDir(), "go.mod")
	if err := os.WriteFile(manifest, []byte("module external"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(manifest, filepath.Join(workDir, "go.mod")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	prepared := claimedWorkspace{Prepared: workspace.Prepared{CWD: workDir}}
	if key := preflightInputKey("setup", preflightInputSources{Loaded: task.Task{}, Profiles: profile.Bundle{}, Prepared: prepared, Executor: task.Executor{}}); key != "" {
		t.Fatal("symlink target freshness was assumed")
	}
}

func TestPreflightReuseTracksStageInputsAndPreservesEditedSkeletons(t *testing.T) {
	for _, change := range []string{"unchanged", "ac", "verification", "quality", "environment", "workdir", "scope", "input", "manifest", "missing-skeleton", "runtime-results", "human-amendment"} {
		t.Run(change, func(t *testing.T) {
			root, workDir := t.TempDir(), t.TempDir()
			executor := task.Executor{CLI: "codex"}
			loaded := task.Task{ID: "fresh", Goal: "implement", Scope: task.Scope{CWD: workDir, AllowedPaths: []string{"."}}, AcceptanceCriteria: []task.AcceptanceCriterion{{ID: "AC1", Text: "behavior", Verification: "check"}}, Preflight: &task.Preflight{AcceptanceSkeleton: &task.AcceptanceSkeletonConfig{Enabled: true}}}
			profiles := profile.Bundle{Quality: &profile.Quality{ID: "quality"}, Environment: &profile.Environment{ID: "environment"}}
			prepared := claimedWorkspace{Prepared: workspace.Prepared{CWD: workDir, Branch: "agent/test"}}
			path := filepath.Join(workDir, "test.go")
			if err := os.WriteFile(path, []byte("implemented test"), 0o600); err != nil {
				t.Fatal(err)
			}
			skeleton := seedPriorPreflightRun(t, priorRunSeed{
				Root: root, Loaded: loaded, Profiles: profiles, Prepared: prepared, Executor: executor,
			})
			wantSetup, wantSkeleton := applyFreshnessChange(t, change, &freshnessInputs{
				Loaded: &loaded, Profiles: &profiles, Prepared: &prepared,
				WorkDir: workDir, SkeletonPath: path, Skeleton: skeleton,
			})
			current := filepath.Join(root, "runs", "fresh-2")
			_, gotSetup, err := reuseReadySetup(preflightReuseQuery{Root: root, TaskID: loaded.ID, RunDir: current, Effective: executor, InputKey: preflightInputKey("setup", preflightInputSources{Loaded: loaded, Profiles: profiles, Prepared: prepared, Executor: executor})})
			if err != nil || gotSetup != wantSetup {
				t.Fatalf("setup reused=%t want=%t: %v", gotSetup, wantSetup, err)
			}
			_, gotSkeleton, err := reuseCompletedAcceptanceSkeleton(preflightReuseQuery{Root: root, TaskID: loaded.ID, RunDir: current, Effective: executor, InputKey: preflightInputKey("skeleton", preflightInputSources{Loaded: loaded, Profiles: profiles, Prepared: prepared, Executor: executor}), WorkDir: prepared.CWD})
			if err != nil || gotSkeleton != wantSkeleton {
				t.Fatalf("skeleton reused=%t want=%t: %v", gotSkeleton, wantSkeleton, err)
			}
			if change != "missing-skeleton" {
				assertSkeletonUnchanged(t, path)
			}
		})
	}
}

// freshnessInputs are the reuse-key inputs one freshness case mutates.
type freshnessInputs struct {
	Loaded       *task.Task
	Profiles     *profile.Bundle
	Prepared     *claimedWorkspace
	WorkDir      string
	SkeletonPath string
	Skeleton     *skeletonpreflight.Result
}

// applyFreshnessChange mutates one reuse-key input and reports whether the
// setup and skeleton stages should still be reusable afterwards.
func applyFreshnessChange(t *testing.T, change string, in *freshnessInputs) (wantSetup, wantSkeleton bool) {
	t.Helper()
	switch change {
	case "unchanged":
		return true, true
	case "ac":
		in.Loaded.AcceptanceCriteria[0].Text = "new requirement"
	case "verification":
		in.Loaded.AcceptanceCriteria[0].Verification = "new check"
	case "quality":
		in.Profiles.Quality.RequiredChecks = []profile.RequiredCheck{{ID: "new", Required: true}}
	case "environment":
		in.Profiles.Environment.Commands = map[string]string{"test": "new command"}
	case "workdir":
		in.Prepared.CWD = t.TempDir()
	case "scope":
		in.Loaded.Scope.ForbiddenPaths = []string{"test.go"}
	case "input":
		in.Prepared.ReviewContractContext.InputFilesDigest = "changed-input"
	case "human-amendment":
		in.Loaded.RevisionRequests = []task.RevisionRequest{{Source: "manual", Text: "new expected behavior", Status: "pending"}}
	case "manifest":
		// A changed manifest invalidates setup discovery but not the skeleton.
		if err := os.WriteFile(filepath.Join(in.WorkDir, "go.mod"), []byte("module changed"), 0o600); err != nil {
			t.Fatal(err)
		}
		return false, true
	case "missing-skeleton":
		// A deleted skeleton invalidates the skeleton stage but not setup.
		if err := os.Remove(in.SkeletonPath); err != nil {
			t.Fatal(err)
		}
		return true, false
	case "runtime-results":
		// Runtime results are excluded from the key, so both stages stay reusable.
		in.Loaded.Status = "failed"
		in.Loaded.AcceptanceCriteria[0].Status = "satisfied"
		skeletonpreflight.ApplyToTask(in.Loaded, in.Skeleton)
		return true, true
	}
	return false, false
}

func assertSkeletonUnchanged(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "implemented test" {
		t.Fatalf("existing work changed: %q %v", data, err)
	}
}

// priorRunSeed is the completed prior run a freshness case reuses from.
type priorRunSeed struct {
	Root     string
	Loaded   task.Task
	Profiles profile.Bundle
	Prepared claimedWorkspace
	Executor task.Executor
}

// seedPriorPreflightRun writes runs/fresh-1 with a ready setup, a completed
// skeleton, and the recorded stage input keys, then returns the skeleton result.
func seedPriorPreflightRun(t *testing.T, seed priorRunSeed) *skeletonpreflight.Result {
	t.Helper()
	prior := filepath.Join(seed.Root, "runs", "fresh-1")
	setup := &setuppreflight.Result{Status: "ready"}
	setuppreflight.ApplyExecutorIdentity(setup, seed.Executor)
	if err := setuppreflight.WriteResult(prior, setup); err != nil {
		t.Fatal(err)
	}
	skeleton := &skeletonpreflight.Result{Status: "completed", Outputs: []skeletonpreflight.Output{{ACID: "AC1", Path: "test.go"}}}
	skeletonpreflight.ApplyExecutorIdentity(skeleton, seed.Executor)
	if err := skeletonpreflight.WriteResult(prior, skeleton); err != nil {
		t.Fatal(err)
	}
	sources := preflightInputSources{Loaded: seed.Loaded, Profiles: seed.Profiles, Prepared: seed.Prepared, Executor: seed.Executor}
	for _, phase := range []string{"setup", "skeleton"} {
		if err := recordPreflightInputs(prior, phase, preflightInputKey(phase, sources), prior); err != nil {
			t.Fatal(err)
		}
	}
	return skeleton
}
