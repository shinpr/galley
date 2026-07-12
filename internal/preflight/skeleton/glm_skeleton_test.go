package skeleton

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shinpr/galley/internal/task"
)

func glmSkeletonOptions(t *testing.T, token string) Options {
	t.Helper()
	return Options{
		Task: task.Task{
			ID:       "task-glm",
			Executor: task.Executor{CLI: "glm", Effort: "high"},
		},
		WorkDir:      t.TempDir(),
		RunDir:       t.TempDir(),
		GLMAuthToken: token,
	}
}

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

func TestBuildBuiltinCreatorCommandPlanGLMRedirects(t *testing.T) {
	cmd, perr := buildBuiltinCreatorCommandPlan(glmSkeletonOptions(t, "zai-token"), []byte("{}"))
	if perr != nil {
		t.Fatalf("buildBuiltinCreatorCommandPlan: %v", perr)
	}
	joined := strings.Join(cmd.EnvAppend, "\n")
	if !strings.Contains(joined, "ANTHROPIC_BASE_URL=https://api.z.ai/api/anthropic") {
		t.Fatalf("EnvAppend missing GLM base URL: %#v", cmd.EnvAppend)
	}
	if !strings.Contains(joined, "ANTHROPIC_AUTH_TOKEN=zai-token") {
		t.Fatalf("EnvAppend missing GLM auth token: %#v", cmd.EnvAppend)
	}
	found := false
	for _, k := range cmd.EnvRemove {
		if k == "ANTHROPIC_API_KEY" {
			found = true
		}
	}
	if !found {
		t.Fatalf("EnvRemove must strip ANTHROPIC_API_KEY: %#v", cmd.EnvRemove)
	}
}

func TestBuildBuiltinCreatorCommandPlanGLMFailsFastWithoutToken(t *testing.T) {
	_, perr := buildBuiltinCreatorCommandPlan(glmSkeletonOptions(t, ""), []byte("{}"))
	if perr == nil {
		t.Fatal("expected fail-fast when glm skeleton creator has no token")
	}
	if !strings.Contains(perr.Error(), "glm_api_key") {
		t.Fatalf("error must name the missing config key, got %q", perr)
	}
}

func TestBuildBuiltinCreatorCommandPlanGrok(t *testing.T) {
	work, runDir := t.TempDir(), t.TempDir()
	cmd, perr := buildBuiltinCreatorCommandPlan(Options{Task: task.Task{ID: "task-grok", Executor: task.Executor{CLI: "grok", Effort: "high"}}, WorkDir: work, RunDir: runDir, GrokBin: "/path/to/grok"}, []byte("{}"))
	if perr != nil {
		t.Fatal(perr)
	}
	joined := strings.Join(cmd.Argv, " ")
	for _, want := range []string{"/path/to/grok", "--prompt-file", "--json-schema", "--permission-mode bypassPermissions", "--sandbox workspace"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("grok creator argv missing %q: %v", want, cmd.Argv)
		}
	}
}
