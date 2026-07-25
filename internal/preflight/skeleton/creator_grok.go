package skeleton

import (
	"github.com/shinpr/galley/internal/proc"
	"github.com/shinpr/galley/internal/runner"
	"github.com/shinpr/galley/prompts"
	"github.com/shinpr/galley/schemas"
)

func buildGrokCreatorCommandPlan(opts Options, payload []byte) (proc.Command, *preflightErr) {
	grokOpts := runner.GrokFromTask(opts.Task)
	grokOpts.Bin = opts.GrokBin
	grokOpts.WorkDir = opts.WorkDir
	grokOpts.Prompt = string(payload)
	grokOpts.SystemPrompt = prompts.AcceptanceSkeletonCreatorGrok()
	grokOpts.JSONSchema = schemas.AcceptanceSkeletonManifest
	grokOpts.PermissionMode = "bypassPermissions"
	grokOpts.AttemptDir = opts.RunDir
	grokOpts.PromptFilename = "grok.acceptance-skeleton.prompt.md"
	plan, err := runner.GrokCommandPlan(grokOpts)
	if err != nil {
		return proc.Command{}, creatorErr("plan built-in creator: %v", err)
	}
	return plan, nil
}
