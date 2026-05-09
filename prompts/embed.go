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

// ClaudeExecutorFull returns the built-in Claude executor system prompt.
func ClaudeExecutorFull() string {
	return claudeExecutorFull
}

// CodexSupervisor returns the built-in Codex supervisor prompt.
func CodexSupervisor() string {
	return supervisorReviewCommon + "\n\n" + codexSupervisorReview
}

// ClaudeSupervisor returns the built-in Claude supervisor system prompt.
func ClaudeSupervisor() string {
	return supervisorReviewCommon + "\n\n" + claudeSupervisorReview
}
