package setup

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/shinpr/galley/internal/runartifact"
	"github.com/shinpr/galley/internal/task"
)

func TestBuildExecutorCommandPlanRoutesGrokWithSchemaPromptFileAndSandbox(t *testing.T) {
	t.Parallel()
	opts := Options{Task: task.Task{ID: "task-grok", Executor: task.Executor{CLI: "grok"}}, WorkDir: t.TempDir(), RunDir: t.TempDir(), GrokBin: "/path/to/grok"}
	cmd, provider, err := BuildExecutorCommandPlan(opts, []byte("{}"))
	if err != nil {
		t.Fatalf("BuildExecutorCommandPlan: %v", err)
	}
	if provider != "grok" || cmd.Argv[0] != "/path/to/grok" {
		t.Fatalf("grok routing = %q %#v", provider, cmd.Argv)
	}
	args := strings.Join(cmd.Argv, " ")
	for _, want := range []string{"--prompt-file", "--json-schema", "--permission-mode bypassPermissions", "--sandbox workspace"} {
		if !strings.Contains(args, want) {
			t.Fatalf("grok setup argv missing %q: %#v", want, cmd.Argv)
		}
	}
}

func TestResolveGrokSetupResultRejectsProseWrappedJSON(t *testing.T) {
	runDir := t.TempDir()
	text, _ := json.Marshal(`prefix {"status":"ready","commands":[]} suffix`)
	envelope := []byte(`{"text":` + string(text) + `,"stopReason":"EndTurn","sessionId":"s"}`)
	if err := os.WriteFile(runartifact.Path(runDir, runartifact.SetupExecutorStdoutFilename), envelope, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := ResolveExecutorResult(Options{Task: task.Task{Executor: task.Executor{CLI: "grok"}}, RunDir: runDir}, string(envelope))
	if err == nil {
		t.Fatal("prose-wrapped Grok setup result accepted")
	}
}
