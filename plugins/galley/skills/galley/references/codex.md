# Codex Runtime Notes

Use this reference when running Galley from Codex CLI, especially evals or daemon execution.

## Permission

Before starting a Galley daemon from Codex, decide whether the daemon command needs elevated sandbox permissions using the rule below.

Use elevated sandbox permissions for `galley daemon run`, `galley daemon run --once`, and `galley daemon start` when the task repository or configured worktree path is outside the current Codex writable roots.

Sibling worktrees such as `../<repo>.worktrees/<task>` commonly need elevation during Codex evals.

Reason: Galley daemon execution writes repository git metadata, sibling worktrees, run evidence, check artifacts, commits, pushes, and PR state. Outside writable roots, these writes can fail with `Operation not permitted`.

Read-only commands such as `galley task validate`, `galley task show`, `galley task list`, and profile validation can run without elevation.

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
| `cannot open '.git/FETCH_HEAD': Operation not permitted` | Codex sandbox blocked `git fetch` in the task repository | Re-run the daemon command with elevated sandbox permissions. |
| `cannot lock ref ... Operation not permitted` | Codex sandbox blocked branch or worktree creation in the task repository | Re-run the daemon command with elevated sandbox permissions. |
| task remains `queued` | daemon did not run, watched another root, or hit a claim conflict | Check `galley daemon status`, `galley task list`, and the daemon root. |
| task remains `running` | active run or stale claim | Check the daemon log and task heartbeat; requeue after claim TTL when stale. |
