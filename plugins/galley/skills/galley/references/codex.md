# Codex Runtime Notes

Use this reference when running Galley from Codex CLI, especially evals or daemon execution.

## Permission

Before starting a Galley daemon from Codex, decide whether the daemon command needs elevated sandbox permissions using the rule below.

Use elevated sandbox permissions for `galley daemon run`, `galley daemon run --once`, and `galley daemon start` when the task repository or configured worktree path is outside the current Codex writable roots.

Sibling worktrees such as `../<repo>.worktrees/<task>` commonly need elevation during Codex evals.

Reason: Galley daemon execution writes repository git metadata, sibling worktrees, run evidence, check artifacts, commits, pushes, and PR state. Outside writable roots, these writes can fail with `Operation not permitted`.

Read-only commands such as `galley task validate`, `galley task show`, `galley task list`, and profile validation can run without elevation.

## Daemon Execution

`galley daemon start` launches a background process and returns after its startup readiness check. Run it with the required sandbox authority, report the command result, and return control to the user.

Stay attached only when the user explicitly requests monitoring or chooses a foreground command such as `galley daemon run --once`. Diagnose queue or run state only after an observed failure, stale state, or explicit status request.

## Common Errors

| Error | Cause | Action |
| --- | --- | --- |
| `operation not permitted` | Codex sandbox blocked worktree, repo, or artifact writes | Re-run the daemon command with elevated sandbox permissions. |
| `cannot open '.git/FETCH_HEAD': Operation not permitted` | Codex sandbox blocked `git fetch` in the task repository | Re-run the daemon command with elevated sandbox permissions. |
| `cannot lock ref ... Operation not permitted` | Codex sandbox blocked branch or worktree creation in the task repository | Re-run the daemon command with elevated sandbox permissions. |
| task remains `queued` | daemon did not run, watched another root, or hit a claim conflict | Check `galley daemon status`, `galley task list`, and the daemon root. |
| task remains `running` | active run or stale claim | Check the daemon log and task heartbeat; requeue after claim TTL when stale. |
