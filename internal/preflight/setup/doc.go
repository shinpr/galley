// Package setup runs the setup executor preflight stage.
//
// Run prepares a fresh task worktree before the acceptance skeleton preflight
// and before the implementation executor. It runs after inputfiles.Prepare so
// the prepared worktree is the state the setup acts on, and before skeleton
// preflight so setup readiness is verified independently of task-specific
// skeleton obligations.
//
// The resolved executor tests or repairs environment setup commands and writes
// setup_result.json. A changed successful plan updates environment.yaml and is
// audited in environment_update.json.
package setup
