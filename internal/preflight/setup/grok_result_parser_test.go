package setup

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/shinpr/galley/internal/runartifact"
	"github.com/shinpr/galley/internal/task"
)

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
