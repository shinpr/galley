package task

import "regexp"

const (
	TaskSchemaPath = "plugins/galley/skills/galley/references/task.schema.json"
)

var (
	validModes               = []string{"afk"}
	validStatuses            = []string{"draft", "queued", "running", "needs_supervisor_review", "accepted", "pr_opened", "failed", "closed", "merged", "archived"}
	validPermissions         = []string{"read-only", "edit", "sandbox-full-access"}
	validPromptModes         = []string{"replace", "append"}
	validAFKDecisionPolicies = []string{"choose-smallest-reversible"}
	validTaskIDPattern       = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
)
