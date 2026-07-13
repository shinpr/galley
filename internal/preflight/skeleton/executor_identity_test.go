package skeleton

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/shinpr/galley/internal/runartifact"
	"github.com/shinpr/galley/internal/task"
)

func TestApplyExecutorIdentityPersistsEmptyModelExplicitly(t *testing.T) {
	t.Parallel()
	runDir := t.TempDir()
	// Empty model is a deliberate resolved value (provider CLI default).
	effective := task.Executor{CLI: "codex", Model: "", Effort: "minimal"}
	res := &Result{
		Status:        "completed",
		SourceOfTruth: true,
		Outputs:       []Output{},
		Baseline:      Baseline{SkeletonHashes: []SkeletonHash{}},
	}
	ApplyExecutorIdentity(res, effective)
	if err := WriteResult(runDir, res); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(runartifact.Path(runDir, runartifact.PreflightResultFilename))
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, key := range []string{`"executor_cli"`, `"executor_model"`, `"executor_effort"`} {
		if !strings.Contains(body, key) {
			t.Fatalf("persisted preflight_result.json missing %s:\n%s", key, body)
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
	if loaded.MatchesExecutor(task.Executor{CLI: "codex", Model: "o4-mini", Effort: "minimal"}) {
		t.Fatal("empty-model evidence must not match non-empty model")
	}
}

func TestApplyExecutorIdentityRoundTripIncludesAllIdentityKeys(t *testing.T) {
	t.Parallel()
	runDir := t.TempDir()
	effective := task.Executor{CLI: "glm", Model: "", Effort: "medium"}
	res := &Result{
		Status:        "completed",
		SourceOfTruth: true,
		Outputs:       []Output{},
		Baseline:      Baseline{SkeletonHashes: []SkeletonHash{}},
	}
	ApplyExecutorIdentity(res, effective)
	if err := WriteResult(runDir, res); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(runartifact.Path(runDir, runartifact.PreflightResultFilename))
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"executor_cli", "executor_model", "executor_effort"} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("decoded JSON missing identity key %q: %#v", key, decoded)
		}
	}
	if decoded["executor_cli"] != "glm" || decoded["executor_model"] != "" || decoded["executor_effort"] != "medium" {
		t.Fatalf("identity fields = cli=%v model=%#v effort=%v", decoded["executor_cli"], decoded["executor_model"], decoded["executor_effort"])
	}
}
