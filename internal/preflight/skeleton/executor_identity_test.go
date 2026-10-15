package skeleton

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/shinpr/galley/internal/runartifact"
	"github.com/shinpr/galley/internal/task"
)

func TestPersistedExecutorIdentityMatchesOnlyTheSameResolvedValues(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		prior     task.Executor
		current   task.Executor
		wantMatch bool
	}{
		{"empty model matches empty model", task.Executor{CLI: "codex", Effort: "minimal"}, task.Executor{CLI: "codex", Effort: "minimal"}, true},
		{"empty model rejects explicit model", task.Executor{CLI: "codex", Effort: "minimal"}, task.Executor{CLI: "codex", Model: "o4-mini", Effort: "minimal"}, false},
		{"empty effort matches empty effort", task.Executor{CLI: "claude", Model: "opus"}, task.Executor{CLI: "claude", Model: "opus"}, true},
		{"empty effort rejects explicit effort", task.Executor{CLI: "claude", Model: "opus"}, task.Executor{CLI: "claude", Model: "opus", Effort: "high"}, false},
		{"explicit effort rejects empty effort", task.Executor{CLI: "claude", Model: "opus", Effort: "high"}, task.Executor{CLI: "claude", Model: "opus"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runDir := t.TempDir()
			res := &Result{Status: "completed", SourceOfTruth: true, Outputs: []Output{}, Baseline: Baseline{SkeletonHashes: []SkeletonHash{}}}
			ApplyExecutorIdentity(res, tc.prior)
			if err := WriteResult(runDir, res); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(runartifact.Path(runDir, runartifact.PreflightResultFilename))
			if err != nil {
				t.Fatal(err)
			}
			var persisted map[string]any
			if err := json.Unmarshal(raw, &persisted); err != nil {
				t.Fatal(err)
			}
			// Empty values are resolved provider defaults, so the keys must persist for reuse matching.
			want := map[string]string{"executor_cli": tc.prior.CLI, "executor_model": tc.prior.Model, "executor_effort": tc.prior.Effort}
			for key, value := range want {
				if got, ok := persisted[key]; !ok || got != value {
					t.Fatalf("persisted %s = %#v (present=%v), want %q:\n%s", key, got, ok, value, raw)
				}
			}
			loaded, err := LoadResult(runDir)
			if err != nil || loaded == nil {
				t.Fatalf("LoadResult = (%+v, %v)", loaded, err)
			}
			if got := loaded.MatchesExecutor(tc.current); got != tc.wantMatch {
				t.Fatalf("MatchesExecutor(%#v) = %v, want %v", tc.current, got, tc.wantMatch)
			}
		})
	}
}
