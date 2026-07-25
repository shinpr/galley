package runner

import (
	"reflect"
	"testing"

	"github.com/shinpr/galley/internal/provider"
)

func TestAppendProviderModelEffortArgs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		transport provider.Transport
		want      []string
	}{
		{provider.TransportClaude, []string{"base", "--model", "m", "--effort", "high"}},
		{provider.TransportCodex, []string{"base", "--model", "m", "-c", `model_reasoning_effort="high"`}},
		{provider.TransportGrok, []string{"base", "--model", "m", "--reasoning-effort", "high"}},
	}
	for _, tt := range tests {
		got := AppendProviderModelEffortArgs([]string{"base"}, tt.transport, "m", "high")
		if !reflect.DeepEqual(got, tt.want) {
			t.Fatalf("%s args got %v, want %v", tt.transport, got, tt.want)
		}
	}
}
