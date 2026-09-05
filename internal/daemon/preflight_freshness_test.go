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
	_, reused, err := reuseReadySetup(root, "freshness", current, executor, "current-inputs")
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
	if key := preflightInputKey("setup", task.Task{}, profile.Bundle{}, prepared, task.Executor{}); key != "" {
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
			prior := filepath.Join(root, "runs", "fresh-1")
			setup := &setuppreflight.Result{Status: "ready"}
			setuppreflight.ApplyExecutorIdentity(setup, executor)
			if err := setuppreflight.WriteResult(prior, setup); err != nil {
				t.Fatal(err)
			}
			skeleton := &skeletonpreflight.Result{Status: "completed", Outputs: []skeletonpreflight.Output{{ACID: "AC1", Path: "test.go"}}}
			skeletonpreflight.ApplyExecutorIdentity(skeleton, executor)
			if err := skeletonpreflight.WriteResult(prior, skeleton); err != nil {
				t.Fatal(err)
			}
			for _, phase := range []string{"setup", "skeleton"} {
				if err := recordPreflightInputs(prior, phase, preflightInputKey(phase, loaded, profiles, prepared, executor), prior); err != nil {
					t.Fatal(err)
				}
			}
			wantSetup, wantSkeleton := false, false
			switch change {
			case "unchanged":
				wantSetup, wantSkeleton = true, true
			case "ac":
				loaded.AcceptanceCriteria[0].Text = "new requirement"
			case "verification":
				loaded.AcceptanceCriteria[0].Verification = "new check"
			case "quality":
				profiles.Quality.RequiredChecks = []profile.RequiredCheck{{ID: "new", Required: true}}
			case "environment":
				profiles.Environment.Commands = map[string]string{"test": "new command"}
			case "workdir":
				prepared.CWD = t.TempDir()
			case "scope":
				loaded.Scope.ForbiddenPaths = []string{"test.go"}
			case "input":
				prepared.ReviewContractContext.InputFilesDigest = "changed-input"
			case "human-amendment":
				loaded.RevisionRequests = []task.RevisionRequest{{Source: "manual", Text: "new expected behavior", Status: "pending"}}
			case "manifest":
				if err := os.WriteFile(filepath.Join(workDir, "go.mod"), []byte("module changed"), 0o600); err != nil {
					t.Fatal(err)
				}
				wantSkeleton = true
			case "missing-skeleton":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				wantSetup = true
			case "runtime-results":
				loaded.Status = "failed"
				loaded.AcceptanceCriteria[0].Status = "satisfied"
				skeletonpreflight.ApplyToTask(&loaded, skeleton)
				wantSetup, wantSkeleton = true, true
			}
			current := filepath.Join(root, "runs", "fresh-2")
			_, gotSetup, err := reuseReadySetup(root, loaded.ID, current, executor, preflightInputKey("setup", loaded, profiles, prepared, executor))
			if err != nil || gotSetup != wantSetup {
				t.Fatalf("setup reused=%t want=%t: %v", gotSetup, wantSetup, err)
			}
			_, gotSkeleton, err := reuseCompletedAcceptanceSkeleton(root, loaded.ID, current, executor, preflightInputKey("skeleton", loaded, profiles, prepared, executor), prepared.CWD)
			if err != nil || gotSkeleton != wantSkeleton {
				t.Fatalf("skeleton reused=%t want=%t: %v", gotSkeleton, wantSkeleton, err)
			}
			if change != "missing-skeleton" {
				if data, err := os.ReadFile(path); err != nil || string(data) != "implemented test" {
					t.Fatalf("existing work changed: %q %v", data, err)
				}
			}
		})
	}
}
