# Operations

This document covers daemon control, task inspection, queue storage, notifications, and process behavior. Use the Galley skill for normal setup and task authoring. For help with a specific failure, start with [Troubleshooting](troubleshooting.md).

## Daily Commands

```sh
galley daemon start
galley daemon status --output json
galley task list
galley task show TASK_ID
galley daemon stop
```

`task list` shows each task's queue state, status, PR URL, latest verdict, and latest summary. `task show` gives the latest attempt, supervisor verdict, risk, failed verification context, and recovery hint.

Use the installed `galley` binary for background daemon control. PID verification records the executable path, so separate `go run ./cmd/galley` invocations cannot reliably start and later stop the same background process.

## Daemon Control

```sh
galley daemon start
galley daemon status --output json
galley daemon stop
galley daemon run --once
```

`daemon start` launches Galley in the background and writes its PID and log under the daemon root. `daemon run --once` drains the tasks currently available in the queue and exits. The background daemon also polls configured PR comments and cleans up worktrees for closed or merged PRs.

The daemon root defaults to `~/.galley`. Use `--root` consistently when running an isolated or test workflow with a different root.

By default, the background files are:

```text
~/.galley/galley-daemon.pid
~/.galley/galley-daemon.log
```

`--pid-file` and `--log-file` override those paths. `daemon stop` sends a normal shutdown request, waits up to `--stop-timeout`, and removes the PID file after it verifies that the recorded process stopped.

## Daemon Configuration

`daemon start` and `daemon run` create `daemon.yaml` under the selected root on first use. Edit this file for durable daemon-wide defaults:

```yaml
supervisor: claude
max_concurrent_tasks: 1
max_concurrent_per_repo: 1
poll_interval: 10s
claim_ttl: 30m
shutdown_timeout: 5m
idle_timeout: 10m
notifications:
  enabled: false
  on: [failed, needs_supervisor_review]
```

| Field | Purpose |
| --- | --- |
| `supervisor` | Daemon-level supervisor backend: `claude`, `codex`, `glm`, or `grok`. A repository environment profile can override it. |
| `max_concurrent_tasks` | Maximum tasks run at once. Must be at least 1. |
| `max_concurrent_per_repo` | Maximum concurrent tasks for one source repository. `0` disables the per-repository limit. |
| `poll_interval` | Delay between queue and maintenance polls. |
| `claim_ttl` | Age used to recover stale task claims; it also determines heartbeat cadence. |
| `shutdown_timeout` | Time active attempts may finish after shutdown is requested. |
| `idle_timeout` | Time an executor or supervisor process may produce no output before Galley kills it. |
| `glm_api_key` | Z.ai credential used when GLM is selected as executor or supervisor. |
| `notifications` | Optional terminal-state command hook described below. |

Explicit flags on `daemon start` or `daemon run` override `daemon.yaml` for that process. Missing file values use built-in defaults. Unknown keys are ignored; invalid values for known keys fail daemon startup.

Repository executor, supervisor, model, and effort defaults belong in `environment.yaml`, not `daemon.yaml`. See [Profiles](profiles.md) and [Models and supervision](supervision.md).

## Task Commands

```sh
galley task validate ./TASK.yaml
galley task work-order ./TASK.yaml
galley task queue ./TASK.yaml --reason "queue for daemon"
galley task list
galley task show TASK_ID
galley task requeue TASK_ID --reason "resolved the reported blocker"
galley task archive ~/.galley/tasks/done/TASK.yaml
```

`task queue` validates a draft, records the queue reason, and copies it into `tasks/queued` without overwriting an existing task. Pass `--move` when the original draft should be removed after queueing.

`task requeue` moves a reviewed task from `failed`, `done`, or `running` back to `queued`. It preserves the existing worktree and applicable review progress. Successful setup and acceptance-skeleton phases are reused when their earlier evidence is still valid.

`task archive` records the archived state, moves the task into `tasks/archived`, and attempts to remove its managed worktree. Archive only tasks that no longer need to resume.

For the authoring contract, see [Task YAML](task-yaml.md). For state-specific decisions, see [Troubleshooting](troubleshooting.md).

## Queue and Evidence Layout

The default daemon root contains:

```text
~/.galley/
  daemon.yaml
  tasks/
    draft/
    queued/
    running/
    done/
    failed/
    archived/
  runs/
  profiles/
    <repo-key>/
      quality.yaml
      environment.yaml
  runtime/
  galley-daemon.pid
  galley-daemon.log
```

Use this command to find a repository's profile directory instead of guessing `<repo-key>`:

```sh
galley profile resolve --cwd /path/to/repo --mkdir --output json
```

Each executor attempt writes evidence under `runs/<run-id>/attempt-N/`. Depending on how far the attempt progressed, it can include:

- `command_plan.json`
- `run_result.json`
- raw provider output
- `executor_result.json`
- `supervisor_verdict.json`
- `git_status.json`
- `diff.patch`
- `workspace.json`
- `profiles.json`
- `supervisor.json`

Run directory names begin with the task ID. Use `task show` to identify the latest attempt number and any reported error artifact path before opening these files. [Troubleshooting](troubleshooting.md#inspecting-run-evidence) maps common questions to the relevant artifact.

## Notifications

The daemon can run an operator-owned command after a task reaches a configured terminal status. Notifications are best-effort and never change task state.

```yaml
notifications:
  enabled: true
  on: [failed, needs_supervisor_review]
  command: "/absolute/path/to/docs/examples/notifications/notify-slack.sh"
```

An empty `on` list uses `[failed, needs_supervisor_review]`. The command may also subscribe to `accepted` and `pr_opened`.

Galley sends one JSON object on stdin:

- `task_id`
- `status`
- `repo`
- `summary`
- `run_dir`
- `show_hint`

The same values are available as `GALLEY_TASK_ID`, `GALLEY_TASK_STATUS`, `GALLEY_REPO`, `GALLEY_SUMMARY`, and `GALLEY_RUN_DIR`. Summary values are limited to 280 runes.

Task data is never inserted into the command string. The hook runs asynchronously with a fixed 30-second timeout. Failures are logged without retry and do not affect the task. Galley does not store webhook URLs or other notification secrets; the command environment owns them.

Sample hooks are in [`docs/examples/notifications/`](examples/notifications/).

## Force Stop

Use `galley daemon stop --force` only when normal stop cannot end a stalled daemon. Galley first tries normal shutdown, then terminates the verified daemon and its recorded executor and supervisor process groups.

Force stop can interrupt active work. The next daemon startup recovers tasks left in `running`; inspect their latest state and evidence before deciding whether to requeue.

On Unix, normal foreground and background shutdown stops new claims and gives active attempts the configured shutdown timeout. On Windows, background stop uses immediate process termination because a console-less process has no SIGTERM equivalent. Run `galley daemon run` in the foreground and stop it with `Ctrl+C` when graceful Windows shutdown is required.

On Windows, `stop --force` terminates the recorded daemon and direct child processes. Descendants created outside those process relationships may require OS-level cleanup.

## Timeouts

Galley uses two independent timeout boundaries:

- daemon `idle_timeout` or `--idle-timeout` stops an executor or supervisor that produces no output
- task `execution_policy.timeout_ms` limits the total wall-clock duration of one executor attempt

Executor idle timeouts are recorded as executor interruptions. A supervisor idle timeout preserves its supervisor artifacts and moves the task to `failed`. Use `task show` before changing a timeout; an authentication or provider failure should be fixed rather than hidden behind a longer limit.

## Filesystems and Concurrency

- Use local filesystems for the daemon root and managed worktrees. Shared filesystems may not provide the rename and timestamp behavior queue claims expect.
- Avoid very short claim TTLs. Filesystems with coarse timestamp resolution can make stale-claim recovery noisy.
- Multiple daemons can share a root because queue claims reject conflicting ownership. Do not share one PID file across unrelated daemon processes.
- Running tasks refresh their ownership record while the executor loop is active. On startup, Galley requeues tasks whose recorded daemon is dead or cannot be verified and leaves tasks owned by a live daemon untouched.
- PR comment polling uses `gh api`. Choose a polling interval appropriate for the number of repositories and the account's GitHub API limits.

Development commands and release procedures are documented in [Contributing](../CONTRIBUTING.md).
