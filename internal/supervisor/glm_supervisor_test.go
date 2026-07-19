package supervisor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunAdapterPayloadGLMRedirectsToEndpointAndStripsAPIKey(t *testing.T) {
	skipPOSIXFakeSupervisorOnWindows(t)
	// Seed a real inherited Anthropic key to prove the redirect strips it from
	// the child so it cannot leak to Z.ai or override the GLM token.
	t.Setenv("ANTHROPIC_API_KEY", "real-anthropic-key")

	binDir := t.TempDir()
	envPath := filepath.Join(t.TempDir(), "claude.env")
	argsPath := filepath.Join(t.TempDir(), "claude.args")
	fakeClaude := filepath.Join(binDir, "claude")
	if err := os.WriteFile(fakeClaude, []byte(`#!/bin/sh
printf 'BASE=%s\nAUTH=%s\nKEY=[%s]\n' "$ANTHROPIC_BASE_URL" "$ANTHROPIC_AUTH_TOKEN" "$ANTHROPIC_API_KEY" > `+envPath+`
printf '%s\n' "$*" > `+argsPath+`
cat >/dev/null
printf '%s\n' '{"status":"accepted","summary":"ok","acceptance_passes":["AC1"],"quality_passes":[],"findings":[],"discussion_items":[]}'
`), 0o700); err != nil {
		t.Fatal(err)
	}

	output, err := RunAdapterPayload(context.Background(), AdapterOptions{
		Provider:     "glm",
		Model:        "provider-model-x",
		WorkDir:      t.TempDir(),
		ArtifactDir:  t.TempDir(),
		ClaudeBin:    fakeClaude,
		GLMAuthToken: "zai-token",
	}, []byte(`{"evidence":{"task":{"id":"task"},"diff":""}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(output), `"status":"accepted"`) {
		t.Fatalf("output got %q", output)
	}
	env, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	got := string(env)
	if !strings.Contains(got, "BASE=https://api.z.ai/api/anthropic") {
		t.Fatalf("child env missing GLM base URL:\n%s", got)
	}
	if !strings.Contains(got, "AUTH=zai-token") {
		t.Fatalf("child env missing GLM auth token:\n%s", got)
	}
	if !strings.Contains(got, "KEY=[]") {
		t.Fatalf("inherited ANTHROPIC_API_KEY was not stripped from child env:\n%s", got)
	}
	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(args), "--model provider-model-x") != 1 {
		t.Fatalf("glm args must contain one configured model: %s", args)
	}
}

func TestRunAdapterPayloadGLMFailsFastWithoutToken(t *testing.T) {
	_, err := RunAdapterPayload(context.Background(), AdapterOptions{
		Provider:    "glm",
		WorkDir:     t.TempDir(),
		ArtifactDir: t.TempDir(),
		ClaudeBin:   filepath.Join(t.TempDir(), "claude-does-not-run"),
	}, []byte(`{"evidence":{"task":{"id":"task"},"diff":""}}`))
	if err == nil {
		t.Fatal("expected glm supervisor without token to fail fast")
	}
	if !strings.Contains(err.Error(), "glm_api_key") {
		t.Fatalf("error must name the missing config key, got %q", err)
	}
}
