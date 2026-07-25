package runner

import (
	"fmt"

	"github.com/shinpr/galley/internal/provider"
)

// AppendProviderModelEffortArgs owns transport-specific model and effort flags.
func AppendProviderModelEffortArgs(args []string, transport provider.Transport, model, effort string) []string {
	if model != "" {
		args = append(args, "--model", model)
	}
	if effort == "" {
		return args
	}
	switch transport {
	case provider.TransportClaude:
		return append(args, "--effort", effort)
	case provider.TransportCodex:
		return append(args, "-c", fmt.Sprintf("model_reasoning_effort=%q", effort))
	case provider.TransportGrok:
		return append(args, "--reasoning-effort", effort)
	default:
		return args
	}
}
