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
5. Check daemon status and resolve the daemon plan.
6. Present the task summary, user-confirmable decisions, validation result, daemon status, and queue plan using the detailed queue approval format in `references/task-authoring.md`.
7. Ask for explicit approval to queue.

For new task YAML that fails with decode or unmarshal errors, preserve the user's goal and AC content, then restore the file shape from the skill-bundled script `scripts/create_task_skeleton.py`. Repair the generated skeleton against `references/task.schema.json` instead of reading Galley source code or inventing struct shapes.

## Queue Flow

Queue only after the user approves the exact task file, user-confirmable decisions, and daemon plan.

Use execution settings approved during task authoring. If the user chose a non-default supervisor such as Codex, carry that choice into the daemon command rather than trying to encode it in the task YAML.

Check daemon status before presenting the final queue approval prompt:

```bash
galley daemon status --output json
```

If the daemon is already running, compare it with the approved execution settings. Surface mismatches that affect the user-visible plan, such as supervisor provider or PR automation. If the daemon is not running, ask whether to start it after queueing or only queue the task. Explain the main choices:

- supervisor: `claude` by default, or `codex` when the user wants Codex review.
- PR automation, PR comment handling, base branch, and worktree cleanup come from the repository `environment.yaml`.
- execution mode: `galley daemon start` for background execution, or `galley daemon run --once` for one queue drain.
- concurrency: use defaults for ordinary tasks; increase parallelism when the user explicitly asks for it.

For daemon-only handoff where a detailed task-authoring summary was already approved, use this compact daemon plan format when no daemon is running:

```markdown
Daemon status:
| Item | Current / planned |
| --- | --- |
| Current daemon | not running |
| Planned supervisor | `<approved-supervisor>` |
| Environment operations | <pr.enabled/pr.base/pr.comments.enabled/pr.comments.reply/worktree.cleanup from environment.yaml> |
| Planned command | `galley daemon start ...` |
| Effect | If approved, the task starts after queueing; otherwise it waits in the queue. |

Queue the task and start this daemon afterward, queue only, or change daemon options?
```

```bash
galley task queue <task-file>
```

After queueing, start the daemon only when the user approved daemon startup. Report the queued path, daemon status, and the exact command used or recommended.

Main path: `galley task queue` targets the running daemon queue, or the default queue when no daemon is running. Drafts outside the daemon root are copied, so the source remains useful for review. Pass `--move` only when the source draft should be removed after queueing.

Advanced roots: pass `--root <path>` only when the user explicitly chose a non-default root.

## Post-Queue PR Review Loop

Use this section when a Galley-created PR needs another executor pass from a PR comment.

A PR comment is treated as a Galley command when the trimmed comment body starts with `/galley`.

Accepted forms:

- `/galley <free-form revision request>`
- `/galley rerun <free-form revision request>`
- `/galley requeue <free-form revision request>`
- `/galley`

The free-form text becomes a pending revision request and the task is requeued when its state allows requeueing. Use concrete revision instructions with acceptance or verification detail. Use normal GitHub comments for discussion that should not trigger another executor run.

Ignored forms:

- Mid-line mentions such as `Looks good, /galley rerun`
- `/galley` after another non-whitespace line
- `/galley:galley ...`
- `/galleyfoo ...`

Trust boundary:

- Galley accepts PR comment commands only from the recorded PR author.
- Keep secrets out of PR comments because the parsed request is stored in task YAML as revision request input.

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
| `executor.cli must be one of: claude, codex` | Use `executor.cli: claude` or `executor.cli: codex`. |
| `execution_policy.loop_budget must be >= 0` | Use an integer greater than or equal to `0`; `0` means unlimited. |

Schema/decode errors such as `field text not found in type task.Decision` mean a nested object has the wrong shape. Use the skill-bundled `references/task.schema.json`. Inspect Galley implementation source only when the task target is Galley itself.

## Approval Prompt

Use the detailed final approval format in `references/task-authoring.md` for new or repaired tasks. It is the canonical queue approval surface because it includes task content, user-confirmable decisions, validation result, daemon settings, and queue plan.

For a task file that was already reviewed in the same conversation, summarize only the queue target and daemon delta, then ask for explicit queue approval. If the user approves, queue the task and report the exact command output.

## Daemon Next Step

For continuous background execution:

```bash
galley daemon start
galley daemon status --output json
```

For a single drain:

```bash
galley daemon run --once
```

Add `--supervisor codex` or `--supervisor claude` when the user selected a non-default supervisor. Keep PR and cleanup behavior in the repository environment profile.
