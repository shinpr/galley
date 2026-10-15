package setup

import (
	"slices"
	"strings"
	"testing"

	"github.com/shinpr/galley/internal/task"
)

func TestBuildExecutorCommandPlanRoutesClaudeTransportProviders(t *testing.T) {
	tests := []struct {
		cli       string
		claudeBin string
		glmToken  string
		kimiKey   string
		wantInEnv []string
		exactEnv  bool
	}{
		{cli: "claude", claudeBin: "/path/to/claude", wantInEnv: []string{"GALLEY_CLAUDE_GUARD_MODE=setup_executor"}, exactEnv: true},
		{cli: "glm", glmToken: "zai-token", wantInEnv: []string{"ANTHROPIC_BASE_URL=https://api.z.ai/api/anthropic", "ANTHROPIC_AUTH_TOKEN=zai-token"}},
		{cli: "kimi", kimiKey: "kimi-token", wantInEnv: []string{"ANTHROPIC_BASE_URL=https://api.kimi.com/coding/", "ANTHROPIC_API_KEY=kimi-token"}},
	}
	for _, tc := range tests {
		t.Run(tc.cli, func(t *testing.T) {
			cmd, provider, err := BuildExecutorCommandPlan(Options{
				Task:         task.Task{ID: "task-" + tc.cli, Executor: task.Executor{CLI: tc.cli, Effort: "high"}},
				WorkDir:      t.TempDir(),
				RunDir:       t.TempDir(),
				ClaudeBin:    tc.claudeBin,
				GLMAuthToken: tc.glmToken,
				KimiAPIKey:   tc.kimiKey,
			}, []byte("{}"))
			if err != nil {
				t.Fatalf("BuildExecutorCommandPlan: %v", err)
			}
			wantBin := tc.claudeBin
			if wantBin == "" {
				wantBin = "claude"
			}
			if provider != "claude" || cmd.Argv[0] != wantBin {
				t.Fatalf("%s must use Claude transport, provider=%q argv=%#v", tc.cli, provider, cmd.Argv)
			}
			if tc.exactEnv && !slices.Equal(cmd.EnvAppend, tc.wantInEnv) {
				t.Fatalf("EnvAppend = %#v, want exactly %#v", cmd.EnvAppend, tc.wantInEnv)
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
