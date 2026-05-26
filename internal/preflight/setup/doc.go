// Package setup runs the setup executor preflight stage.
//
// Run prepares a fresh task worktree before the acceptance skeleton preflight
// and before the implementation executor. It runs after inputfiles.Prepare so
// the prepared worktree is the state the setup acts on, and before skeleton
// preflight so setup readiness is verified independently of task-specific
// skeleton obligations.
//
// The daemon delegates setup execution to the task executor provider
// (Claude or Codex per task.executor.cli). environment.setup.commands, when
// present, is passed as a prior plan for the setup executor to try, diagnose,
// and repair in the same model context. On success the daemon persists
// runs/<run-id>/setup_result.json and, when the successful plan differs from
// the resolved environment profile, atomically rewrites the repository
// environment.yaml setup field and records the change in
// runs/<run-id>/environment_update.json.
package setup
