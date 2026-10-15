package setup

import (
	"strings"
	"testing"

	"github.com/shinpr/galley/internal/task"
)

func TestBuildExecutorCommandPlanRedirectsClaudeProviders(t *testing.T) {
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
			cmd, provider, err := BuildExecutorCommandPlan(Options{
				Task:         task.Task{ID: "task-" + tc.cli, Executor: task.Executor{CLI: tc.cli, Effort: "high"}},
				WorkDir:      t.TempDir(),
				RunDir:       t.TempDir(),
				GLMAuthToken: tc.glmToken,
				KimiAPIKey:   tc.kimiKey,
			}, []byte("{}"))
			if err != nil {
				t.Fatalf("BuildExecutorCommandPlan: %v", err)
			}
			if provider != "claude" || cmd.Argv[0] != "claude" {
				t.Fatalf("%s must use Claude transport, provider=%q argv=%#v", tc.cli, provider, cmd.Argv)
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
