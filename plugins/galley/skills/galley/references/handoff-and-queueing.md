# Handoff and Queueing

Use this reference when validating, repairing, or queueing a Galley task file.

## Validation Flow

1. Locate the task file.
2. Confirm it is a draft task file.
3. Run validation:

```bash
galley task validate <task-file>
```

4. Repair YAML or task fields until validation succeeds.
5. Present the task summary and validation result.
6. Ask for explicit approval to queue.

## Queue Flow

Queue only after the user approves the exact task file.

```bash
galley task queue <task-file>
```

After queueing, report the queued path and the daemon command the user can run.

Advanced roots: `galley task queue` normally targets the running daemon queue, or the default queue when no daemon is running. Pass `--root <path>` only when the user explicitly chose a non-default root. Pass `--move` only when the source draft file should be removed after queueing.

## Common Validation Fixes

| Validation Message | Fix |
| --- | --- |
| `scope.cwd must be an absolute path` | Replace with the absolute target repo path. |
| `scope.allowed_paths must contain at least one relative path` | Add the narrowest relative path set. |
| `acceptance_criteria must contain at least one criterion` | Add ACs with `id`, `text`, `verification`, and `status`. |
| `worktree.enabled must be true for AFK tasks` | Set `worktree.enabled: true`. |
| `worktree.branch is required for AFK tasks` | Add a valid branch such as `agent/<task-id>`. |
| `worktree.path must point to a sibling path outside scope.cwd` | Use a relative sibling path such as `../<repo-name>.worktrees/<task-name>`. |
| `files[n].source is required` | Add the source path or remove the file entry. |
| `files[n].destination is required` | Add the destination path where Galley should copy the input file in the worktree. |
| `files[n].destination must stay within scope.allowed_paths` | Move the destination under an allowed path or expand `scope.allowed_paths` deliberately. |
| `executor.cli must be claude` | Use `executor.cli: claude`. |
| `execution_policy.loop_budget must be positive` | Use a positive integer or `infinite`. |

## Approval Prompt

Use this concise approval prompt:

```markdown
Validation passed for `<task-file>`.

Queue target:
- repo: `<scope.cwd>`
- branch: `<worktree.branch>`
- worktree: `<worktree.path>`
- review iteration: `<supervisor.review_iterations>`
- loop budget: `<execution_policy.loop_budget>`
- reference files: `<none or files[].destination with commit policy>`

Approve queueing this task?
```

If the user approves, queue the task and report the exact command output.

## Daemon Next Step

For a single drain:

```bash
galley daemon run --once
```

For continuous background execution:

```bash
galley daemon start
galley daemon status --output json
```

Add flags such as `--open-pr`, `--poll-pr-comments`, and `--supervisor codex` or `--supervisor claude` according to the setup profile.
