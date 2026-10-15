package runner

import "testing"

func TestClaudeCommandPlanEmptyEffortEmitsNoOverride(t *testing.T) {
	t.Parallel()
	command, err := ClaudeCommandPlanForOS(ClaudeOptions{
		Model:          "opus",
		Effort:         "",
		PermissionMode: "bypassPermissions",
		Prompt:         "do the work",
	}, "linux")
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range command.Argv {
		if a == "--effort" {
			t.Fatalf("empty effort must not emit --effort; argv=%v", command.Argv)
		}
	}
}
