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
		model     string
		effort    string
		want      []string
	}{
		{provider.TransportClaude, "m", "high", []string{"base", "--model", "m", "--effort", "high"}},
		{provider.TransportCodex, "m", "high", []string{"base", "--model", "m", "-c", `model_reasoning_effort="high"`}},
		{provider.TransportGrok, "m", "high", []string{"base", "--model", "m", "--reasoning-effort", "high"}},
		{provider.TransportClaude, "", "", []string{"base"}},
	}
	for _, tt := range tests {
		got := AppendProviderModelEffortArgs([]string{"base"}, tt.transport, tt.model, tt.effort)
		if !reflect.DeepEqual(got, tt.want) {
			t.Fatalf("%s args got %v, want %v", tt.transport, got, tt.want)
		}
	}
}
