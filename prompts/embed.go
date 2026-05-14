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

// ClaudeExecutorFull returns the built-in Claude executor system prompt.
func ClaudeExecutorFull() string {
	return claudeExecutorFull
}

// CodexExecutorFull returns the built-in Codex executor system prompt.
func CodexExecutorFull() string {
	return codexExecutorFull
}

// AcceptanceSkeletonCreator returns the built-in test-skeleton creator prompt.
func AcceptanceSkeletonCreator() string {
	return acceptanceSkeletonCreator
}

// CodexSupervisor returns the built-in Codex supervisor prompt.
func CodexSupervisor() string {
	return supervisorReviewCommon + "\n\n" + codexSupervisorReview
}

// ClaudeSupervisor returns the built-in Claude supervisor system prompt.
func ClaudeSupervisor() string {
	return supervisorReviewCommon + "\n\n" + claudeSupervisorReview
}
