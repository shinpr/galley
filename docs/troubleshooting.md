# Troubleshooting

Start with the task, not the daemon log. Galley records the latest actionable state on the task and keeps detailed evidence for deeper inspection.

```sh
galley task list
galley task show TASK_ID
galley task show TASK_ID --output json
galley daemon status --output json
```

`task list` locates the task and shows its current state. `task show` explains the latest attempt, verdict, error, risk, and recovery hint; JSON output includes the complete persisted task state. Check the daemon only when queued work is not being claimed or the task state does not match the running process.

## What the Task State Means

| State or status | Meaning | Next action |
| --- | --- | --- |
| `draft` | The task has not been queued. | Review and validate it, then queue it. |
| `queued` | The task is waiting for a daemon. | Confirm the daemon is running and uses the same root. |
| `running` | A daemon owns the task. | Let the attempt finish. If its daemon is gone, restart the daemon to recover the task. |
| `needs_supervisor_review` | The loop cannot continue until a person makes the decision named in the latest verdict. | Resolve that product, design, authority, or task-premise decision, then requeue. |
| `failed` with `latest_executor_interruption: true` | The executor did not reach a reliable normal terminal. The supervisor was not run. | Resolve the provider or runtime problem, then requeue the retained worktree. |
| `failed` | Galley reached a hard stop or another terminal failure. | Read the latest error and risk before deciding whether the task can safely resume. |
| `accepted` | The work passed review and remains local. | Inspect the worktree or archive the task when it is no longer needed. |
| `pr_opened` | The accepted work was committed, pushed, and opened as a pull request. | Continue normal review in GitHub. |
| `closed` or `merged` | The recorded pull request reached its terminal GitHub state. | No action is normally required. |

## A Queued Task Does Not Start

Check the daemon and its root:

```sh
galley daemon status --output json
galley daemon start
```

The task and daemon must use the same Galley root. The default is `~/.galley`; custom roots must be supplied consistently. If the daemon exits during startup, inspect `galley-daemon.log` and validate `daemon.yaml` values.

If the daemon is running, `galley task show TASK_ID` usually exposes profile loading, workspace creation, or task validation failures after the task is claimed.

## Executor Interruption

An interruption means the selected provider or local runtime did not produce a reliable normal terminal. Common causes include provider outages, authentication failures, CLI startup errors, idle timeouts, process kills, and machine shutdown.

Galley does not send interrupted output to the supervisor or retry it within the same run. It preserves tracked and untracked worktree changes and records the provider terminal details when available.

Malformed executor JSON after a normal provider terminal is different: Galley keeps the diff reviewable and lets the supervisor decide whether another executor attempt is useful. `task show` marks only the pre-terminal failure path as `latest_executor_interruption: true`.

After resolving the cause, resume the same worktree:

```sh
galley task requeue TASK_ID --reason "resolved executor interruption"
```

Requeueing is appropriate when the task direction is unchanged and the retained work is still useful. Queue a new task instead when the goal, repository, or acceptance contract has materially changed.

## Supervisor Review Is Required

`needs_supervisor_review` is a task result, not a daemon crash. Start with:

```sh
galley task show TASK_ID
```

The latest verdict summary names the decision the loop cannot make. Typical cases are:

- acceptance criteria conflict and require a product decision
- repository evidence contradicts a task premise
- an authoritative human request is ambiguous
- required authority or destructive approval is missing

Update the task or authoritative source that owns the decision, then requeue with a reason that records what changed. Provider, timeout, exhausted-loop, and finalization failures use `failed`; follow the recorded error and risk instead of treating them as review decisions.

## A Running Task Outlives Its Daemon

On startup, Galley recovers running tasks whose recorded owner is dead or cannot be verified. Start the daemon and check the task again:

```sh
galley daemon start
galley task show TASK_ID
```

A normal daemon stop lets active attempts finish until the shutdown timeout. Use force stop only for a stalled daemon:

```sh
galley daemon stop --force
```

Successful force stop moves tasks owned by that daemon to `failed` and preserves their worktrees and evidence. Inspect each task, then use the existing requeue or archive workflow:

```sh
galley task show TASK_ID
galley task requeue TASK.yaml --reason "environment recovered"
galley task archive TASK.yaml --reason "discard interrupted work"
```

See [Operations](operations.md#force-stop) for platform-specific process behavior.

## PR Creation or Comment Handling Fails

PR automation depends on an authenticated GitHub CLI and network access:

```sh
gh auth status
```

Use `task show` to distinguish an accepted implementation from a commit, push, or `gh pr create` failure. Fix the GitHub or repository condition before requeueing. For comment requeues, confirm that comments are enabled in `environment.yaml`, the daemon is running, the PR remains open, and the command comes from the recorded PR author.

See [PR automation](pr-automation.md) for accepted command forms and cleanup behavior.

## Inspecting Run Evidence

Run directories begin with the task ID under `~/.galley/runs/`. `task show` reports the latest attempt number and, for errors that have a dedicated artifact directory, prints that path directly. Start with the artifact that matches the question:

| Question | Evidence |
| --- | --- |
| What command did Galley run? | `command_plan.json` |
| Did the provider process start, time out, or exit? | `run_result.json` and raw provider output |
| What did the executor report? | `executor_result.json` |
| Why did the supervisor accept or reject the work? | `supervisor_verdict.json` |
| What changed in the worktree? | `git_status.json` and `diff.patch` |
| Which profiles and workspace were used? | `profiles.json` and `workspace.json` |
| Which supervisor model and setting source were used? | `supervisor.json` |

Not every attempt produces every file. For example, an executor interruption records no supervisor verdict because review was intentionally skipped.
