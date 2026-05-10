package daemon

import (
	"fmt"
	"strings"

	"github.com/shinpr/galley/internal/task"
)

const acceptanceSkeletonVerificationMarker = "Acceptance skeleton:"

func applyAcceptanceSkeletonResultToTask(loaded *task.Task, res *AcceptanceSkeletonResult) {
	if loaded == nil || res == nil || loaded.Preflight == nil || loaded.Preflight.AcceptanceSkeleton == nil {
		return
	}
	cfg := loaded.Preflight.AcceptanceSkeleton
	cfg.Outputs = make([]task.AcceptanceSkeletonOutputDef, 0, len(res.Outputs))
	for _, out := range res.Outputs {
		cfg.Outputs = append(cfg.Outputs, task.AcceptanceSkeletonOutputDef{
			ACID:                   out.ACID,
			Path:                   out.Path,
			Kind:                   out.Kind,
			Purpose:                out.Purpose,
			Satisfies:              out.Satisfies,
			IntegrationPoint:       out.IntegrationPoint,
			ImplementationRequired: out.ImplementationRequired,
		})
	}
	for i := range loaded.AcceptanceCriteria {
		loaded.AcceptanceCriteria[i].Verification = mergeSkeletonVerification(loaded.AcceptanceCriteria[i].Verification, loaded.AcceptanceCriteria[i].ID, res.Outputs)
	}
}

func mergeSkeletonVerification(existing, acID string, outputs []AcceptanceSkeletonOutput) string {
	var lines []string
	for _, out := range outputs {
		if out.ACID != acID {
			continue
		}
		lines = append(lines, fmt.Sprintf("- `%s`: %s", out.Path, out.Purpose))
		if out.Satisfies != "" {
			lines = append(lines, fmt.Sprintf("  satisfies: %s", out.Satisfies))
		}
		if out.IntegrationPoint != "" {
			lines = append(lines, fmt.Sprintf("  integration point: %s", out.IntegrationPoint))
		}
	}
	if len(lines) == 0 {
		return existing
	}
	base := strings.TrimSpace(existing)
	if strings.HasPrefix(base, acceptanceSkeletonVerificationMarker+"\n") || base == acceptanceSkeletonVerificationMarker {
		base = ""
	} else if idx := strings.Index(base, "\n\n"+acceptanceSkeletonVerificationMarker); idx >= 0 {
		base = strings.TrimSpace(base[:idx])
	}
	block := acceptanceSkeletonVerificationMarker + "\n" + strings.Join(lines, "\n")
	if base == "" {
		return block
	}
	return base + "\n\n" + block
}
