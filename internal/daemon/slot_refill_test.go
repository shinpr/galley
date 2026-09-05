package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shinpr/galley/internal/queue"
	"github.com/shinpr/galley/internal/task"
)

func TestFreedSlotStartsWaitingTaskBeforeLongTaskEnds(t *testing.T) {
	testSlotRefill(t, false)
}

func TestActiveSlotRecoversStaleClaimsWithoutRequeuingOwnTask(t *testing.T) {
	testSlotRefill(t, true)
}

func testSlotRefill(t *testing.T, recoverStale bool) {
	t.Helper()
	root := t.TempDir()
	promptPath, schemaPath := writeDaemonPromptFiles(t)
	if err := queue.EnsureLayout(root); err != nil {
		t.Fatal(err)
	}
	markers := t.TempDir()
	longStarted, thirdStarted, release := filepath.Join(markers, "long"), filepath.Join(markers, "third"), filepath.Join(markers, "release")
	bin := writeFakeClaude(t, "case \"$PWD\" in\n"+
		"*/long) touch "+shellPath(longStarted)+"; while [ ! -f "+shellPath(release)+" ]; do sleep 0.01; done;;\n"+
		"*/third) touch "+shellPath(thirdStarted)+";;\nesac\n"+
		"echo change > daemon-output.txt\necho '{\"status\":\"completed\",\"summary\":\"done\",\"files_modified\":[\"daemon-output.txt\"],\"acceptance_criteria\":[{\"id\":\"AC1\",\"status\":\"satisfied\",\"evidence\":[\"diff\"],\"notes\":\"done\"}],\"verification\":[],\"scope_expansions\":[],\"decisions\":[],\"risks\":[]}'\n")
	for i, name := range []string{"long", "short", "third"} {
		path := filepath.Join(root, "tasks", "queued", string(rune('a'+i))+".yaml")
		if recoverStale && name == "third" {
			path = filepath.Join(root, "tasks", "running", "c.yaml")
		}
		writeDaemonTask(t, path, initDaemonGitRepo(t))
		loaded, err := task.Load(path)
		if err != nil {
			t.Fatal(err)
		}
		loaded.ID = name
		loaded.ExecutionPolicy.TimeoutMS = 15000
		loaded.Worktree.Path = filepath.Join("..", "worktrees", name)
		if err := task.Save(path, loaded); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- runTestDaemon(ctx, Options{Root: root, SystemPromptFile: promptPath, JSONSchemaFile: schemaPath, Once: true, MaxConcurrentTasks: 2, ClaudeBin: bin, Supervisor: "claude", PollInterval: 20 * time.Millisecond, ShutdownTimeout: time.Millisecond})
	}()
	defer func() { _ = os.WriteFile(release, nil, 0o600); cancel(); <-done }()
	deadline := time.Now().Add(5 * time.Second)
	madeStale := false
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			done <- err
			t.Fatalf("daemon exited before refill: %v", err)
		default:
		}
		if recoverStale && !madeStale {
			if _, err := os.Stat(longStarted); err == nil {
				old := time.Now().Add(-2 * time.Hour)
				for _, name := range []string{"a.yaml", "c.yaml"} {
					if err := os.Chtimes(filepath.Join(root, "tasks", "running", name), old, old); err != nil {
						t.Fatal(err)
					}
				}
				madeStale = true
			}
		}
		if _, err := os.Stat(thirdStarted); err == nil {
			if _, err := os.Stat(longStarted); err != nil {
				t.Fatal("long task did not start")
			}
			if _, err := os.Stat(filepath.Join(root, "tasks", "running", "a.yaml")); err != nil {
				t.Fatal("long task no longer running")
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("free slot stayed idle while the long task was running")
}
