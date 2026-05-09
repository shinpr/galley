# Codex Runtime Notes

Use this reference when running Galley from Codex CLI, especially evals or daemon execution.

## Permission

Run task authoring, validation, and queueing normally.

Use elevated sandbox permissions for `galley daemon start` or `galley daemon run --once` when the task needs paths outside the current Codex writable roots:

- `scope.cwd`
- `worktree.path`
- required check output directories
- git or GitHub operations used by PR automation

Sibling worktrees such as `../<repo>.worktrees/<task>` commonly need elevation during Codex evals.

## Daemon Execution

When running the daemon from Codex, operate as a broker and carry one run to a terminal state.

1. Confirm the task is queued with `galley task show <task-id-or-file>`.
2. Start the daemon command with a tool timeout long enough for the task.
3. Stay attached until the task reaches `done`, `failed`, or the command times out.
4. Report the final task status from `galley task show <task-id>`.

## Common Errors

| Error | Cause | Action |
| --- | --- | --- |
| `operation not permitted` | Codex sandbox blocked worktree, repo, or artifact writes | Re-run the daemon command with elevated sandbox permissions. |
| task remains `queued` | daemon did not run, watched another root, or hit a claim conflict | Check `galley daemon status`, `galley task list`, and the daemon root. |
| task remains `running` | active run or stale claim | Check the daemon log and task heartbeat; requeue after claim TTL when stale. |
