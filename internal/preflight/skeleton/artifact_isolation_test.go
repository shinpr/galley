package skeleton

import (
	"bytes"
	"os"
	"testing"

	"github.com/shinpr/galley/internal/preflight/setup"
	"github.com/shinpr/galley/internal/task"
)

func TestCodexSetupAndSkeletonKeepDistinctRawArtifacts(t *testing.T) {
	runDir, workDir := t.TempDir(), t.TempDir()
	task := task.Task{Executor: task.Executor{CLI: "codex"}}
	setupPlan, _, err := setup.BuildExecutorCommandPlan(setup.Options{Task: task, RunDir: runDir, WorkDir: workDir}, []byte("{}"))
	if err != nil {
		t.Fatal(err)
	}
	arg := func(argv []string, key string) string {
		for i := 0; i+1 < len(argv); i++ {
			if argv[i] == key {
				return argv[i+1]
			}
		}
		t.Fatalf("missing %s in %q", key, argv)
		return ""
	}
	schema := arg(setupPlan.Argv, "--output-schema")
	before, err := os.ReadFile(schema)
	if err != nil {
		t.Fatal(err)
	}
	setupMessage := arg(setupPlan.Argv, "--output-last-message")
	if err := os.WriteFile(setupMessage, []byte(`{"status":"ready","commands":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	skeletonPlan, perr := buildCodexCreatorCommandPlan(Options{Task: task, RunDir: runDir, WorkDir: workDir}, []byte("{}"))
	if perr != nil {
		t.Fatal(perr)
	}
	if err := os.WriteFile(arg(skeletonPlan.Argv, "--output-last-message"), []byte(`{"outputs":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(schema)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("skeleton overwrote setup schema")
	}
	res, err := setup.ResolveExecutorResult(setup.Options{Task: task, RunDir: runDir}, "")
	if err != nil || res.Status != "ready" {
		t.Fatalf("lost setup result: %#v %v", res, err)
	}
}
