package skeleton

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shinpr/galley/internal/task"
)

func TestResolveGrokCreatorManifestRejectsProseWrappedJSON(t *testing.T) {
	runDir := t.TempDir()
	manifest := `{"outputs":[],"no_skeletons":[]}`
	text, _ := json.Marshal("prefix " + manifest + " suffix")
	envelope := `{"text":` + string(text) + `,"stopReason":"EndTurn","sessionId":"s"}`
	stdoutPath := filepath.Join(runDir, "grok.stdout.json")
	if err := os.WriteFile(stdoutPath, []byte(envelope), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := resolveCreatorManifest(Options{Task: task.Task{Executor: task.Executor{CLI: "grok"}}, RunDir: runDir}, envelope, stdoutPath)
	if err == nil {
		t.Fatal("prose-wrapped Grok manifest accepted")
	}
}

func TestBuildBuiltinCreatorCommandPlanRedirectsClaudeProviders(t *testing.T) {
	tests := []struct {
		cli       string
		glmToken  string
		kimiKey   string
		wantInEnv []string
	}{
		{cli: "glm", glmToken: "zai-token", wantInEnv: []string{"ANTHROPIC_BASE_URL=https://api.z.ai/api/anthropic", "ANTHROPIC_AUTH_TOKEN=zai-token"}},
		{cli: "kimi", kimiKey: "kimi-token", wantInEnv: []string{"ANTHROPIC_BASE_URL=https://api.kimi.com/coding/", "ANTHROPIC_API_KEY=kimi-token"}},
	}
	for _, tc := range tests {
		t.Run(tc.cli, func(t *testing.T) {
			cmd, perr := buildBuiltinCreatorCommandPlan(Options{
				Task:         task.Task{ID: "task-" + tc.cli, Executor: task.Executor{CLI: tc.cli, Effort: "high"}},
				WorkDir:      t.TempDir(),
				RunDir:       t.TempDir(),
				GLMAuthToken: tc.glmToken,
				KimiAPIKey:   tc.kimiKey,
			}, []byte("{}"))
			if perr != nil {
				t.Fatalf("buildBuiltinCreatorCommandPlan: %v", perr)
			}
			joined := strings.Join(cmd.EnvAppend, "\n")
			for _, want := range tc.wantInEnv {
				if !strings.Contains(joined, want) {
					t.Fatalf("EnvAppend missing %q: %#v", want, cmd.EnvAppend)
				}
			}
		})
	}
}
