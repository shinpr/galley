package setup

import (
	"testing"

	"github.com/shinpr/galley/internal/task"
)

// TestBuildExecutorCommandPlanCodexPassesExpandedEfforts proves AC4 for the
// setup executor role: each officially supported Codex effort, including the
// newly accepted minimal/xhigh/max, reaches the setup command plan unchanged as
// a quoted model_reasoning_effort override.
func TestBuildExecutorCommandPlanCodexPassesExpandedEfforts(t *testing.T) {
	for _, effort := range []string{"minimal", "low", "medium", "high", "xhigh", "max"} {
		effort := effort
		t.Run(effort, func(t *testing.T) {
			opts := Options{
				Task: task.Task{
					ID:       "task-codex-setup",
					Executor: task.Executor{CLI: "codex", Model: "gpt-5-codex", Effort: effort},
				},
				WorkDir: t.TempDir(),
				RunDir:  t.TempDir(),
			}
			cmd, provider, err := BuildExecutorCommandPlan(opts, []byte("{}"))
			if err != nil {
				t.Fatalf("BuildExecutorCommandPlan: %v", err)
			}
			if provider != "codex" {
				t.Fatalf("provider = %q, want codex", provider)
			}
			want := `model_reasoning_effort="` + effort + `"`
			found := false
			for i := 0; i+1 < len(cmd.Argv); i++ {
				if cmd.Argv[i] == "-c" && cmd.Argv[i+1] == want {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("setup codex argv missing quoted literal %q: %v", want, cmd.Argv)
			}
		})
	}
}
