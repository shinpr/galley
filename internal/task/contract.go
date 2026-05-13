package task

import "regexp"

const (
	TaskSchemaPath = "plugins/galley/skills/galley/references/task.schema.json"
)

var (
	validModes                  = []string{"afk"}
	validStatuses               = []string{"draft", "queued", "running", "needs_supervisor_review", "accepted", "pr_opened", "failed", "closed", "merged", "archived"}
	validPermissions            = []string{"read-only", "edit", "sandbox-full-access"}
	validPromptModes            = []string{"replace", "append"}
	validExecutorCLIs           = []string{"claude", "codex"}
	validAFKDecisionPolicies    = []string{"choose-smallest-reversible"}
	validPreflightSkeletonModes = []string{"skeleton"}
	validTaskIDPattern          = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
)

// ExecutorCLIEnum returns the supported executor.cli values in the stable order
// used by both task validation and the generated JSON schema enum. The accessor
// exists so test code and any future consumers can verify the contract surface
// without duplicating the literal slice.
func ExecutorCLIEnum() []string {
	out := make([]string, len(validExecutorCLIs))
	copy(out, validExecutorCLIs)
	return out
}
