# Operations

This document covers day-to-day Galley operation: queue layout, task commands, daemon control, recovery behavior, and local development checks.

For normal task authoring, prefer the Galley skill. It validates task YAML and profiles before queueing.

## Queue Layout

The daemon root defaults to `~/.galley`:

```text
~/.galley/
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
    claude-guard-plugin/
  galley-daemon.pid
  galley-daemon.log
```

`profiles/` holds repository quality and environment YAML files. Galley resolves `<repo-key>` from `scope.cwd`; use `galley profile resolve --cwd <repo> --mkdir --output json` to find the exact paths and create their parent directory.

Each executor attempt writes evidence under `runs/<run-id>/attempt-N/`, where `<run-id>` is generated as `<task-id>-<unix-nano>`.

Attempt evidence includes:

- `command_plan.json`
- `run_result.json`
- `executor_result.json` when the executor returns valid structured JSON
- `supervisor_verdict.json`
- `git_status.json`
- `diff.patch`

Galley also records `workspace.json` for the effective execution workspace and writes `profiles.json` when quality or environment profiles are loaded.

## Task Files

For hand-authored task YAML, use [task-yaml.md](task-yaml.md) as the reference.

```sh
galley task list
galley task show TASK_ID
galley task validate ./TASK.yaml
galley task work-order ./TASK.yaml
galley task queue ./TASK.yaml --reason "queue for daemon"
galley task requeue TASK_ID --reason "addressed review feedback"
galley task archive ~/.galley/tasks/done/TASK.yaml
```

`galley task queue` validates a `draft` task, sets `status: queued`, records a queue attempt, and writes it into `tasks/queued` without overwriting an existing queued file. Drafts outside the daemon root are copied by default; pass `--move` when the source file should be removed after queueing.

`galley task list` shows task state, status, PR URL, latest verdict, and latest summary across the workflow root.

`galley task show` accepts a task file or task ID and prints the latest attempt, supervisor verdict, risk, and failed verification context. Accepted terminal tasks show prior attempt errors as audit history, not active failures.

`galley task requeue` accepts a task ID or task file, returns a reviewed task from `tasks/failed`, `tasks/done`, or `tasks/running` to `tasks/queued`, records an optional reason, and increments `supervisor.review_iterations`.

## Profiles

Use `galley profile` commands to validate profiles or locate the repository-specific profile directory.

```sh
galley profile validate --kind quality examples/quality-default.yaml
galley profile validate --kind environment examples/environment-local.yaml
galley profile resolve --cwd /path/to/repo --mkdir --output json
```

See [profiles.md](profiles.md) for supported fields and examples.

## Daemon

```sh
galley daemon start
galley daemon status --output json
galley daemon stop
galley daemon run --once
```

`galley daemon run --once` drains queued tasks once and exits. Background `galley daemon start` also performs daemon maintenance such as PR comment polling and closed or merged PR worktree cleanup according to `environment.yaml`.

For PR automation and cleanup details, see [pr-automation.md](pr-automation.md).

`--root` points at the daemon root and defaults to `~/.galley`. Use `--root .agent-workflow` only for repo-local or test workflows.

`galley daemon start` launches the daemon in the background. It writes a PID file and appends stdout and stderr to a log file. By default those files are under `~/.galley`; override them with `--pid-file` and `--log-file`.

`galley daemon stop` reads the PID file, sends `SIGTERM`, waits up to `--stop-timeout`, and removes the PID file when it still points at the stopped process. `galley daemon status` reports whether the PID file points at a live process.

Use the installed `galley` binary for `start`, `status`, and `stop`. PID verification records the executable path, so `go run ./cmd/galley ... daemon start` is not suitable for background daemon control because later `go run` invocations use different temporary binaries.

### Daemon startup defaults (`daemon.yaml`)

Both `galley daemon run` and `galley daemon start` create `daemon.yaml` under the selected daemon root on first use. The file holds durable startup defaults such as `supervisor`, concurrency, polling, timeout, and notification settings. `galley daemon status` and `galley daemon stop` are read-only.

CLI flags on `galley daemon run` or `galley daemon start` always override the matching `daemon.yaml` field for that run (including `--shutdown-timeout`). Anything you do not set on the CLI falls back to `daemon.yaml`; anything `daemon.yaml` does not set falls back to the built-in default.

### Notifications

The daemon can run an opt-in, best-effort command hook after a task reaches a terminal published status. Notifications are configured only in `daemon.yaml`.

```yaml
notifications:
  enabled: true
  on: [failed, needs_supervisor_review]
  command: "/absolute/path/to/docs/examples/notifications/notify-slack.sh"
```

Fields:

- `enabled` (bool, default `false`): gates the hook. When `true`, `command` must be set.
- `on` (list, optional): terminal task statuses that trigger the hook. Empty means `[failed, needs_supervisor_review]`.
- `command` (string): the operator-owned shell command to run.

#### Hook input

Task-derived data is passed to the command as data, never as part of the command string. Each invocation receives:

- A single-line JSON object on stdin with these fields:
  - `task_id` — the task ID.
  - `status` — the terminal published status that triggered the hook.
  - `repo` — the task `scope.cwd`.
  - `summary` — a short human-readable summary (latest attempt summary, falling back to the latest risk detail, then the task goal), truncated to 280 runes with a trailing `…` when clipped.
  - `run_dir` — the run evidence directory for the attempt, when available.
  - `show_hint` — the inspection command an operator can run, `galley task show <task_id>`.
- The same values mirrored as namespaced environment variables: `GALLEY_TASK_ID`, `GALLEY_TASK_STATUS`, `GALLEY_REPO`, `GALLEY_SUMMARY` (also truncated to 280 runes), and `GALLEY_RUN_DIR`.

#### Timeout and failure behavior

The hook is best-effort and never changes task state. It runs asynchronously, has a fixed 30-second timeout, and logs failures without retrying delivery. During daemon shutdown, in-flight notifications may be canceled.

#### Security boundary

The `command` string is operator-owned. Task content is untrusted and is delivered only through stdin JSON and `GALLEY_*` environment variables. Sample hooks live under [`docs/examples/notifications/`](examples/notifications/).

#### Non-goals

- No built-in Slack, email, desktop, or other native notifier. Galley only runs the operator-provided command.
- No secrets storage. Any webhook URL, token, or credential is owned by the operator's command/environment (for example `SLACK_WEBHOOK_URL` exported for `notify-slack.sh`); Galley never holds or persists the secret.
- No operator-tunable timeout, retries, or delivery guarantees.

### Supervisor resolution

`--supervisor` selects the built-in supervisor adapter (`claude`, `codex`, or `glm`). `glm` runs the `claude` binary against GLM's Z.ai endpoint and needs a `glm_api_key` in `daemon.yaml`. The daemon resolves the supervisor for each task in this order:

1. The repository `environment.yaml` `supervisor.default_cli` (resolved from `scope.cwd`). This overrides every layer below for that task only.
2. The daemon CLI `--supervisor` startup flag.
3. The `supervisor` field in `daemon.yaml`.
4. The built-in default (`claude`).

`galley daemon status` omits startup defaults it cannot report accurately, such as effective supervisor and concurrency. `runs/<run-id>/supervisor.json` records the adapter that ran, its model, and whether the model came from `environment.yaml` or the provider CLI default.

Set `supervisor.model` in the repository profile to pass an exact model name to every built-in supervisor evaluation. See [profiles.md](profiles.md#environmentyaml) for fallback behavior.

On Unix, foreground and background daemons use the same shutdown path. On `SIGINT` or `SIGTERM`, Galley stops claiming new queued tasks, lets active attempts finish until the shutdown timeout, records evidence, and avoids starting another retry attempt after shutdown is requested.

### Windows

Windows has no SIGTERM equivalent that can be delivered to a console-less background process, so background `galley daemon start`/`stop` performs an immediate `TerminateProcess` rather than a graceful shutdown:

- `galley daemon status` uses `OpenProcess` + `GetExitCodeProcess` to verify the recorded PID instead of the Unix `signal(0)` probe. `STILL_ACTIVE` (Windows exit code 259) reports alive; any other exit code reports stopped.
- `galley daemon stop` terminates the daemon PID directly. Active executor/supervisor attempts running under that daemon do not get a chance to record graceful-shutdown evidence on Windows; the next daemon startup recovers any interrupted running task.
- `galley daemon stop --force` terminates the daemon plus every recorded executor/supervisor child. On Windows, descendant processes spawned by those children may require OS-level cleanup.
- For a graceful shutdown on Windows, run `galley daemon run` in the foreground and stop it with `Ctrl+C`. The foreground daemon shutdown path is the same on every OS: it stops claiming new tasks, lets active attempts finish until `--shutdown-timeout`, and records evidence.

## Force Stop

`galley daemon stop --force` is an escape hatch for a stalled daemon. It:

1. Tries the normal graceful shutdown first.
2. Re-verifies process identity and sends `SIGKILL` when the daemon has not exited within `--stop-timeout`.
3. After the daemon is gone, sends `SIGKILL` to every registered executor and supervisor child process group recorded under the daemon root, waiting up to the same `--stop-timeout`.
4. If any child process group is still alive after that wait, prints an error naming the surviving PIDs and PGIDs and leaves the PID file in place so the operator can target the same daemon record.

A force kill can interrupt an active attempt; the next daemon startup recovers the interrupted running task.

## Timeouts

Galley uses two timeout concepts:

- `--idle-timeout`: kills executor or built-in supervisor subprocesses that stop producing output.
- `execution_policy.timeout_ms`: bounds the total wall-clock duration of one executor attempt.

Executor idle timeouts are recorded as `error_kind: idle_timeout` and as `idle_timed_out: true` in run evidence, then the task loop continues according to the task loop budget.

Built-in supervisor subprocess failures caused by idle timeout, total timeout, or forced kill are retried up to two additional times inside the same executor attempt. Each try writes evidence under `runs/<run-id>/attempt-N/supervisor-try-<M>/`.

If every supervisor try is killed by the idle-output watchdog, Galley records `error_phase: supervisor` and `error_kind: supervisor_idle_timeout`. Requeue the task, or adjust `--idle-timeout` / `--supervisor`.

## Operational Notes

### Filesystems

- Galley is intended for trusted local repositories and local filesystems. Queue claims rely on no-overwrite file creation plus atomic rename behavior on the same filesystem.
- No-overwrite queue/task moves (`task queue`, `task requeue`, archive, and daemon claim/requeue) use `O_CREATE|O_EXCL` rather than `os.Link`, so they no longer require the destination filesystem to implement hardlinks. Duplicate destinations are still rejected on every supported OS.
- Shared network filesystems may not provide the same rename and mtime behavior as a local disk.
- Avoid very short `--claim-ttl` values. Filesystems with coarse mtime resolution can make overly aggressive stale-claim detection noisy.

### Concurrency and Recovery

- Running multiple daemon processes is supported by claim conflict handling.
- Background control uses a local PID file. Avoid sharing the same `--pid-file` across unrelated processes, and prefer one workflow root per managed daemon.
- Running tasks heartbeat their YAML mtime while the executor loop is active. `--heartbeat-interval` defaults to `min(claim-ttl/4, 1m)`.
- Each claimed running task records the owning daemon. On startup the daemon immediately requeues running tasks whose recorded owner is dead or cannot be verified, while leaving tasks still owned by a verified live daemon untouched.

### External Services

- PR comment polling uses `gh api`; choose a polling interval that respects GitHub API rate limits for your account and repository count.
- Closed or merged PR cleanup treats the task worktree as disposable execution state. Keep anything that should survive cleanup outside the task worktree.
- Executor process failures and infrastructure errors are reported after the current queue is drained.

## Development

### Schema References

```sh
galley schema check
galley schema generate
```

`galley schema generate` writes the task, quality profile, and environment profile schemas into the packaged skill references. `galley schema check` verifies those reference files still match the Go contracts and is run in CI.

The `examples/` directory is for Galley checkout development and CI validation. Normal users should prefer `~/.galley` tasks created by the plugin skill.

Development build and tests:

```sh
go test ./...
go build ./cmd/galley
```

Run the local smoke test:

```sh
./scripts/smoke-local.sh
```

The smoke test builds the binaries, creates a temporary git repository, installs a fake `claude` executable, queues a draft AFK task, runs the daemon once, and verifies that the task reaches `done/accepted` with run evidence.

```sh
galley task validate examples/afk-task.yaml
galley task work-order examples/afk-task.yaml
galley daemon run --once
```

For local development and release notes, see [../CONTRIBUTING.md](../CONTRIBUTING.md) and [../CHANGELOG.md](../CHANGELOG.md).

Release assets are built by GitHub Actions when a GitHub Release is published. See [../.github/workflows/release.yml](../.github/workflows/release.yml) and [../.goreleaser.yaml](../.goreleaser.yaml).
