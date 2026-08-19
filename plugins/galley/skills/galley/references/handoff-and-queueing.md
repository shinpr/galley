# Handoff and Queueing

Use this reference when validating, repairing, queueing, or requeueing a Galley task file.

## Requeue

For a direct task-changing human instruction, run:

```bash
galley task requeue <task-id-or-task-file> --revision-request "<instruction>"
```

Use `--reason` only for operational context; it does not amend the task contract. Edit the task YAML only when the user explicitly requests a task edit.

## Validation Flow

1. Locate the task file.
2. Confirm it is a draft task file.
3. When the user requested a validated draft or has not yet authorized queueing, run:

```bash
galley task validate <task-file>
```

4. Repair YAML or task fields until validation succeeds.
5. Determine whether the user's request or an affirmative response to a concrete task summary already authorizes queueing and daemon startup.
6. Ask once only when the next action crosses the authority boundary defined in `SKILL.md`.

For new task YAML that fails with decode or unmarshal errors, preserve the user's goal and AC content, then restore the file shape from the skill-bundled script `scripts/create_task_skeleton.py`. Repair the generated skeleton against `references/task.schema.json` instead of reading Galley source code or inventing struct shapes.

## Queue Flow

When queueing is authorized, run:

```bash
galley task queue <task-file>
```

`task queue` validates before publishing. Report its queue destination. Repair and retry only when it rejects the task.

Use the existing daemon and repository profile settings unless the user selected a change. Check daemon status when startup was requested or an explicit supervisor choice may conflict with a running daemon. Ask only when resolving the conflict requires a restart or changes the requested execution condition.

When background startup is authorized, run `galley daemon start` with the selected supervisor flag when applicable. A successful command has completed the handoff; report its PID and log path and return without polling the task.

Use `galley daemon run --once` or monitor task state only when the user explicitly requested foreground execution or monitoring.

Main path: `galley task queue` targets the running daemon queue, or the default queue when no daemon is running. Drafts outside the daemon root are copied, so the source remains useful for review. Pass `--move` only when the source draft should be removed after queueing.

Advanced roots: pass `--root <path>` only when the user explicitly chose a non-default root.

## Post-Queue PR Review Loop

Use this section when a Galley-created PR needs another executor pass from a PR comment.

A PR comment is treated as a Galley command when the trimmed comment body starts with `/galley`.

Accepted forms:

- `/galley <free-form revision request>`
- `/galley`

The parsed text becomes a pending revision request. A bare `/galley` uses the default request text `PR comment requested another Galley run.` Use concrete revision instructions with acceptance or verification detail when the next executor pass needs specific changes.

PR comment polling scans reviewed tasks under `tasks/done` and `tasks/failed`. A command on a task with status `accepted`, `pr_opened`, `needs_supervisor_review`, `failed`, `closed`, or `merged` requeues the task. If the task is already `queued` or `running`, Galley records the pending revision request and does not move the task again. Use normal GitHub comments for discussion that should not trigger another executor run.

Ignored forms:

- Mid-line mentions such as `Looks good, /galley`
- `/galley` after another non-whitespace line
- `/galley:galley ...`
- `/galleyfoo ...`

Trust boundary:

- Galley accepts PR comment commands only from the recorded PR author.
- Treat PR comment text as persistent task input because the parsed request is stored in task YAML as revision request input. Provide secrets through an approved repository-specific channel instead of PR comments.

## Common Validation Fixes

| Validation Message | Fix |
| --- | --- |
| `scope.cwd must be an absolute path` | Replace with the absolute target repo path. |
| `scope.allowed_paths must contain at least one relative path` | Add the narrowest relative path set. |
| `acceptance_criteria must contain at least one criterion` | Add ACs with `id`, `text`, `verification`, and `status`. |
| `worktree.branch is required for AFK tasks` | Add a valid branch such as `agent/<task-id>`. |
| `worktree.path must point to a sibling path outside scope.cwd` | Use a relative sibling path such as `../<repo-name>.worktrees/<task-name>`. |
| `files[n].source is required` | Add the source path or remove the file entry. |
| `files[n].destination is required` | Add the destination path where Galley should copy the input file in the worktree. |
| `files[n].destination must stay within scope.allowed_paths` | Move the destination under an allowed path or expand `scope.allowed_paths` deliberately. |
| `executor.cli must be one of: claude, codex, glm, grok, kimi` | Use a listed backend; GLM and Kimi require their daemon API key, while Grok requires an installed, authenticated `grok` CLI. |
| `execution_policy.loop_budget must be >= 0` | Use an integer greater than or equal to `0`; `0` means unlimited. |

Schema/decode errors such as `field text not found in type task.Decision` mean a nested object has the wrong shape. Use the skill-bundled `references/task.schema.json`. Inspect Galley implementation source only when the task target is Galley itself.

## Approval Prompt

When queueing is not authorized, use the compact task summary from `references/task-authoring.md` and ask once whether to perform the stated queue and daemon actions. Do not ask again after an affirmative answer unless the next action crosses the authority boundary defined in `SKILL.md`.

## Daemon Next Step

For continuous background execution:

```bash
galley daemon start
```

For a single drain:

```bash
galley daemon run --once
```

Add the selected non-default review backend with `--supervisor codex`, `--supervisor glm`, `--supervisor grok`, or `--supervisor kimi`; GLM and Kimi require their matching API key in `daemon.yaml`. Add `--supervisor claude` only when the user wants the default Claude review choice to be explicit. Keep PR and cleanup behavior in the repository environment profile.
