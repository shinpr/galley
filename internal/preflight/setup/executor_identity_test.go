package setup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shinpr/galley/internal/runartifact"
	"github.com/shinpr/galley/internal/task"
)

func TestApplyExecutorIdentityPersistsEmptyModelExplicitly(t *testing.T) {
	t.Parallel()
	runDir := t.TempDir()
	// Empty model is a deliberate resolved value (provider CLI default), not
	// "field absent". Persistence must keep the key so requeue reuse can match.
	effective := task.Executor{CLI: "claude", Model: "", Effort: "high"}
	res := &Result{
		Status:            StatusReady,
		Commands:          []CommandAttempt{},
		ReadinessEvidence: "ready",
		Source:            SourceDiscovered,
	}
	ApplyExecutorIdentity(res, effective)
	if err := WriteResult(runDir, res); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(runartifact.Path(runDir, runartifact.SetupResultFilename))
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, key := range []string{`"executor_cli"`, `"executor_model"`, `"executor_effort"`} {
		if !strings.Contains(body, key) {
			t.Fatalf("persisted setup_result.json missing %s:\n%s", key, body)
		}
	}
	if !strings.Contains(body, `"executor_model": ""`) && !strings.Contains(body, `"executor_model":""`) {
		t.Fatalf("empty executor_model must be present as empty string, not omitted:\n%s", body)
	}

	loaded, err := LoadResult(runDir)
	if err != nil || loaded == nil {
		t.Fatalf("LoadResult = (%+v, %v)", loaded, err)
	}
	if !loaded.MatchesExecutor(effective) {
		t.Fatalf("loaded identity %#v must match effective %#v", loaded.ResolvedExecutor(), effective)
	}
	if loaded.ExecutorModel != "" {
		t.Fatalf("ExecutorModel = %q, want empty", loaded.ExecutorModel)
	}
	if loaded.Provider != "claude" {
		t.Fatalf("Provider mirror = %q, want claude", loaded.Provider)
	}

	// Presence of empty model must not match a later non-empty model.
	if loaded.MatchesExecutor(task.Executor{CLI: "claude", Model: "sonnet", Effort: "high"}) {
		t.Fatal("empty-model evidence must not match non-empty model")
	}
}

func TestApplyExecutorIdentityRoundTripIncludesAllIdentityKeys(t *testing.T) {
	t.Parallel()
	runDir := t.TempDir()
	effective := task.Executor{CLI: "grok", Model: "grok-code", Effort: "low"}
	res := &Result{Status: StatusReady, Commands: []CommandAttempt{}}
	ApplyExecutorIdentity(res, effective)
	if err := WriteResult(runDir, res); err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	data, err := os.ReadFile(filepath.Join(runDir, runartifact.SetupResultFilename))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"executor_cli", "executor_model", "executor_effort"} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("decoded JSON missing identity key %q: %#v", key, decoded)
		}
	}
	if decoded["executor_cli"] != "grok" || decoded["executor_model"] != "grok-code" || decoded["executor_effort"] != "low" {
		t.Fatalf("identity fields = %#v", decoded)
	}
}
