package task

import (
	"strings"
	"testing"

	"github.com/shinpr/galley/internal/profile"
)

func TestRenderWorkOrderWithProfiles(t *testing.T) {
	loaded, err := Load(writeTaskYAML(t, "loop_budget: 3"))
	if err != nil {
		t.Fatal(err)
	}
	workOrder := RenderWorkOrderWithProfiles(loaded, profile.Bundle{
		Quality: &profile.Quality{
			ID: "strict",
			RequiredChecks: []profile.RequiredCheck{
				{ID: "tests", PreferredCommands: []string{"go test ./..."}, Required: true},
			},
			PassPolicy: profile.PassPolicy{MinScore: 90},
		},
		Environment: &profile.Environment{
			ID:       "local",
			CWD:      loaded.Scope.CWD,
			Commands: map[string]string{"build": "go build ./cmd/galley"},
			Constraints: profile.Constraints{
				Network:             "approval_required",
				SecretsPolicy:       "never_read_env_files",
				DestructiveCommands: "deny",
			},
		},
	})
	for _, want := range []string{"## Quality Profile", "go test ./...", "## Environment Profile", "never_read_env_files"} {
		if !strings.Contains(workOrder, want) {
			t.Fatalf("work order missing %q:\n%s", want, workOrder)
		}
	}
}
