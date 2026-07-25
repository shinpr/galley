package runner

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/shinpr/galley/internal/proc"
)

// writeFakeExecutorClaude writes a POSIX shell fake `claude` executor that dumps
// its argv and the load-bearing ANTHROPIC_* environment entries to files, then
// emits a minimal completed executor-result JSON to stdout. It mirrors the
// fake-binary approach in internal/supervisor/adapter_test.go but exercises the
// executor side of the command plan, which is otherwise only asserted by argv
// string-equality with no real process ever consuming the plan.
func writeFakeExecutorClaude(t *testing.T, argvPath, envPath string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell fake executor binary")
	}
	bin := filepath.Join(t.TempDir(), "claude")
	body := "#!/bin/sh\n" +
		"printf '%s\\n' \"$@\" > " + shellQuoteForTest(argvPath) + "\n" +
		"{\n" +
		"  printf 'BASE_URL=%s\\n' \"$ANTHROPIC_BASE_URL\"\n" +
		"  printf 'AUTH_TOKEN=%s\\n' \"$ANTHROPIC_AUTH_TOKEN\"\n" +
		"  printf 'API_KEY=%s\\n' \"$ANTHROPIC_API_KEY\"\n" +
		"} > " + shellQuoteForTest(envPath) + "\n" +
		"cat >/dev/null\n" +
		"printf '%s\\n' '{\"status\":\"completed\",\"summary\":\"done\",\"files_modified\":[],\"acceptance_criteria\":[],\"verification\":[],\"scope_expansions\":[],\"decisions\":[],\"risks\":[]}'\n"
	if err := os.WriteFile(bin, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	return bin
}

func shellQuoteForTest(path string) string {
	return "'" + strings.ReplaceAll(path, "'", `'\''`) + "'"
}

// TestClaudeExecutorCommandPlanDeliversLoadBearingFlagsToRealProcess verifies
// the planned flags survive an actual child-process boundary.
func TestClaudeExecutorCommandPlanDeliversLoadBearingFlagsToRealProcess(t *testing.T) {
	argvPath := filepath.Join(t.TempDir(), "argv")
	envPath := filepath.Join(t.TempDir(), "env")
	bin := writeFakeExecutorClaude(t, argvPath, envPath)

	plan, err := ClaudeCommandPlan(ClaudeOptions{
		Bin:     bin,
		Effort:  "high",
		WorkDir: t.TempDir(),
		Prompt:  "implement the work order",
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := proc.RunCommand(context.Background(), plan, proc.RunOptions{})
	if err != nil {
		t.Fatalf("proc.RunCommand: %v\nstderr: %s", err, result.Stderr)
	}
	if !strings.Contains(result.Stdout, `"status":"completed"`) {
		t.Fatalf("executor result JSON not returned: %q", result.Stdout)
	}

	argv, err := os.ReadFile(argvPath)
	if err != nil {
		t.Fatal(err)
	}
	got := string(argv)
	for _, want := range []string{"--effort", "high", "--system-prompt", "--json-schema"} {
		if !strings.Contains(got, want) {
			t.Fatalf("executor argv missing %q:\n%s", want, got)
		}
	}
}

// TestGLMExecutorCommandPlanRedirectsChildEnvironment applies the GLM redirect to
// a built Claude executor plan and runs it, asserting the child process observes
// the GLM endpoint and token via the environment while the inherited Anthropic
// key is stripped. This is the executor-side mirror of the supervisor
// fake-binary tests; the redirect helper has a pure unit test on the plan, but
// nothing proves the env transformation reaches a real child.
func TestGLMExecutorCommandPlanRedirectsChildEnvironment(t *testing.T) {
	argvPath := filepath.Join(t.TempDir(), "argv")
	envPath := filepath.Join(t.TempDir(), "env")
	bin := writeFakeExecutorClaude(t, argvPath, envPath)
	// Seed a real inherited Anthropic key: the redirect must strip it from the
	// child so a stale key cannot override the GLM token or leak to Z.ai.
	t.Setenv("ANTHROPIC_API_KEY", "real-anthropic-key")

	const token = "zai-secret-token"
	plan, err := ClaudeCommandPlan(ClaudeOptions{
		Bin:     bin,
		Effort:  "high",
		WorkDir: t.TempDir(),
		Prompt:  "implement the work order",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := ConfigureClaudeProvider(&plan, ClaudeProviderOptions{
		Provider:    "glm",
		Credentials: ClaudeCredentials{GLMAuthToken: token},
	}); err != nil {
		t.Fatal(err)
	}

	result, err := proc.RunCommand(context.Background(), plan, proc.RunOptions{})
	if err != nil {
		t.Fatalf("proc.RunCommand: %v\nstderr: %s", err, result.Stderr)
	}

	envData, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	env := string(envData)
	for _, want := range []string{
		"BASE_URL=" + GLMBaseURL,
		"AUTH_TOKEN=" + token,
		"API_KEY=\n",
	} {
		if !strings.Contains(env, want) {
			t.Fatalf("child env missing %q:\n%s", want, env)
		}
	}

	// The token must never reach argv, where it would appear in process listings
	// and command-plan evidence.
	argvData, err := os.ReadFile(argvPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(argvData), token) {
		t.Fatalf("GLM token leaked onto executor argv:\n%s", argvData)
	}
}
