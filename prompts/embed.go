package prompts

import _ "embed"

//go:embed supervisor-review-common.md
var supervisorReviewCommon string

//go:embed codex-supervisor-review.md
var codexSupervisorReview string

//go:embed claude-supervisor-review.md
var claudeSupervisorReview string

//go:embed claude-executor-full.md
var claudeExecutorFull string

//go:embed codex-executor-full.md
var codexExecutorFull string

//go:embed acceptance-skeleton-creator.md
var acceptanceSkeletonCreator string

//go:embed acceptance-skeleton-creator-codex.md
var acceptanceSkeletonCreatorCodex string

// ClaudeExecutorFull returns the built-in Claude executor system prompt.
func ClaudeExecutorFull() string {
	return claudeExecutorFull
}

// CodexExecutorFull returns the built-in Codex executor system prompt.
func CodexExecutorFull() string {
	return codexExecutorFull
}

// AcceptanceSkeletonCreator returns the built-in test-skeleton creator prompt
// used when the task implementation executor backend is Claude.
func AcceptanceSkeletonCreator() string {
	return acceptanceSkeletonCreator
}

// AcceptanceSkeletonCreatorCodex returns the Codex provider variant of the
// built-in test-skeleton creator prompt. It is used when the task
// implementation executor backend (task.executor.cli) is Codex. The output
// contract and quality bar match AcceptanceSkeletonCreator; only the runtime
// framing and tool guidance follow Codex conventions.
func AcceptanceSkeletonCreatorCodex() string {
	return acceptanceSkeletonCreatorCodex
}

// CodexSupervisor returns the built-in Codex supervisor prompt.
func CodexSupervisor() string {
	return supervisorReviewCommon + "\n\n" + codexSupervisorReview
}

// ClaudeSupervisor returns the built-in Claude supervisor system prompt.
func ClaudeSupervisor() string {
	return supervisorReviewCommon + "\n\n" + claudeSupervisorReview
}
