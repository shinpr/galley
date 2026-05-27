package runner

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestRunCommandInheritsParentEnvironmentByDefault(t *testing.T) {
	t.Setenv("GO_WANT_GALLEY_ENV_HELPER", "1")
	t.Setenv("GALLEY_ISSUE_75_TEST_KEY", "parent-value")

	result, err := RunCommand(context.Background(), envHelperCommand(t, "GALLEY_ISSUE_75_TEST_KEY"), RunOptions{})
	if err != nil {
		t.Fatalf("RunCommand: %v\nstderr: %s", err, result.Stderr)
	}
	if got := strings.TrimSpace(result.Stdout); got != "GALLEY_ISSUE_75_TEST_KEY=parent-value" {
		t.Fatalf("helper stdout got %q", got)
	}
}

func TestRunCommandAppendsGalleyOwnedEnvWithoutPersistingParentEnv(t *testing.T) {
	t.Setenv("GO_WANT_GALLEY_ENV_HELPER", "1")
	t.Setenv("GALLEY_ISSUE_75_TEST_KEY", "parent-value")
	t.Setenv("GALLEY_CLAUDE_GUARD_MODE", "parent-mode")

	command := envHelperCommand(t, "GALLEY_ISSUE_75_TEST_KEY", "GALLEY_CLAUDE_GUARD_MODE")
	command.EnvAppend = []string{"GALLEY_CLAUDE_GUARD_MODE=supervisor"}

	result, err := RunCommand(context.Background(), command, RunOptions{})
	if err != nil {
		t.Fatalf("RunCommand: %v\nstderr: %s", err, result.Stderr)
	}
	lines := strings.Split(strings.TrimSpace(result.Stdout), "\n")
	want := []string{
		"GALLEY_ISSUE_75_TEST_KEY=parent-value",
		"GALLEY_CLAUDE_GUARD_MODE=supervisor",
	}
	if len(lines) != len(want) {
		t.Fatalf("helper stdout lines got %v, want %v", lines, want)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Fatalf("helper stdout line %d got %q, want %q", i, lines[i], want[i])
		}
	}

	encoded, err := json.Marshal(command)
	if err != nil {
		t.Fatalf("marshal command: %v", err)
	}
	if strings.Contains(string(encoded), "supervisor") || strings.Contains(string(encoded), "parent-value") {
		t.Fatalf("command JSON leaked environment data: %s", encoded)
	}
}

func envHelperCommand(t *testing.T, keys ...string) Command {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	argv := []string{exe, "-test.run=TestGalleyEnvHelperProcess", "--"}
	argv = append(argv, keys...)
	return Command{Argv: argv}
}

func TestGalleyEnvHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_GALLEY_ENV_HELPER") != "1" {
		return
	}
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	if len(args) > 0 {
		args = args[1:]
	}
	for _, key := range args {
		_, _ = os.Stdout.WriteString(key + "=" + os.Getenv(key) + "\n")
	}
	os.Exit(0)
}
