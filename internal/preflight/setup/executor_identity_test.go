package setup

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
		{"empty model matches empty model", task.Executor{CLI: "claude", Effort: "high"}, task.Executor{CLI: "claude", Effort: "high"}, true},
		{"empty model rejects explicit model", task.Executor{CLI: "claude", Effort: "high"}, task.Executor{CLI: "claude", Model: "sonnet", Effort: "high"}, false},
		{"empty effort matches empty effort", task.Executor{CLI: "claude", Model: "opus"}, task.Executor{CLI: "claude", Model: "opus"}, true},
		{"empty effort rejects explicit effort", task.Executor{CLI: "claude", Model: "opus"}, task.Executor{CLI: "claude", Model: "opus", Effort: "high"}, false},
		{"explicit effort rejects empty effort", task.Executor{CLI: "claude", Model: "opus", Effort: "high"}, task.Executor{CLI: "claude", Model: "opus"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			loaded := persistAndLoadExecutorIdentity(t, tc.prior)
			if loaded.Provider != tc.prior.CLI {
				t.Fatalf("Provider mirror = %q, want %q", loaded.Provider, tc.prior.CLI)
			}
			if got := loaded.MatchesExecutor(tc.current); got != tc.wantMatch {
				t.Fatalf("MatchesExecutor(%#v) = %v, want %v", tc.current, got, tc.wantMatch)
			}
		})
	}
}

// persistAndLoadExecutorIdentity writes setup_result.json for exec and reloads it,
// requiring every identity key to persist even when its resolved value is empty.
func persistAndLoadExecutorIdentity(t *testing.T, exec task.Executor) *Result {
	t.Helper()
	runDir := t.TempDir()
	res := &Result{Status: StatusReady, Commands: []CommandAttempt{}, ReadinessEvidence: "ready", Source: SourceDiscovered}
	ApplyExecutorIdentity(res, exec)
	if err := WriteResult(runDir, res); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(runartifact.Path(runDir, runartifact.SetupResultFilename))
	if err != nil {
		t.Fatal(err)
	}
	var persisted map[string]any
	if err := json.Unmarshal(raw, &persisted); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"executor_cli": exec.CLI, "executor_model": exec.Model, "executor_effort": exec.Effort}
	for key, value := range want {
		if got, ok := persisted[key]; !ok || got != value {
			t.Fatalf("persisted %s = %#v (present=%v), want %q:\n%s", key, got, ok, value, raw)
		}
	}
	loaded, err := LoadResult(runDir)
	if err != nil || loaded == nil {
		t.Fatalf("LoadResult = (%+v, %v)", loaded, err)
	}
	return loaded
}
