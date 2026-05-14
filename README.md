![Galley blueprint](docs/assets/galley-blueprint.jpg)

# Galley

[![Claude Code](https://img.shields.io/badge/Claude%20Code-Plugin-purple)](https://claude.ai/code)
[![Codex CLI](https://img.shields.io/badge/Codex%20CLI-Compatible-10a37f)](https://developers.openai.com/codex/cli)
[![Agent Skills](https://img.shields.io/badge/Agent%20Skills-Spec%20Compliant-blue)](https://developers.openai.com/codex/skills/)
[![CI](https://github.com/shinpr/galley/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/shinpr/galley/actions/workflows/ci.yml)
[![GitHub Release](https://img.shields.io/github/v/release/shinpr/galley)](https://github.com/shinpr/galley/releases)
[![License: MIT](https://img.shields.io/github/license/shinpr/galley)](LICENSE)

Galley is a local orchestration runtime for supervised AI agent task execution.

It runs locally, keeps work in git-visible changes, and records evidence for review before each acceptance decision.

Galley defaults to Claude Code for execution and supervisor review. Task YAML can select Codex as the executor, and the daemon can select Codex as the supervisor.

## Quick Start

Use the Galley skill to set up each repository. It walks you through CLI installation when needed, prepares repository profiles, drafts valid task YAML, and queues tasks only after approval.

Install the plugin in Claude Code:

```text
/plugin marketplace add shinpr/galley
/plugin install galley@galley-tools
/reload-plugins
/galley:galley Set up Galley for this repository.
```

Or install it in Codex:

```sh
codex plugin marketplace add shinpr/galley
```

Then start or return to Codex, open the plugin picker, install `Galley`, and ask the skill to set up the repository:

```text
/plugins
$galley Set up Galley for this repository.
```

For a task, describe the work to the skill:

```text
/galley:galley Create a Galley task for this feature request and queue it after approval.
```

## Manual CLI Installation

Most users should start with the skill. Use these commands when installing the binary manually, scripting setup, or debugging the CLI outside the skill workflow. Installing only the binary does not create repository profiles or start the daemon for a project.

Install the latest GitHub Release binary:

```sh
curl -fsSL https://raw.githubusercontent.com/shinpr/galley/main/scripts/install.sh | sh
```

Or use Go directly:

```sh
go install github.com/shinpr/galley/cmd/galley@latest
```

After manual installation, run the setup skill for the repository you want Galley to manage.

Use the CLI directly for status checks and daemon control:

```sh
galley --help
galley daemon start
galley daemon status --output json
galley daemon stop
galley daemon run --once
```

## Skill Capabilities

Galley includes a plugin that packages one Agent Skill for Claude Code and Codex. This is the recommended setup and authoring path: install the plugin, ask the skill to inspect your repository, and let it handle profiles, task YAML, validation steps, queueing commands, and troubleshooting guidance.

Profiles are worth setting up early. They tell Galley which commands are available, which quality checks are required, what evidence the supervisor should expect, and which findings should block acceptance. The skill can create these interactively from the target repository.

- **Task authoring**: clarify the user goal, target repo, scope, input files, acceptance criteria, verification, loop budget, supervisor, and PR behavior; write a draft task YAML, validate it, and queue it only after explicit user approval.
- **Profile authoring**: interactively create `quality.yaml` and `environment.yaml` for repo-specific quality gates, commands, tools, network policy, secrets policy, PR behavior, and cleanup policy.
- **Setup**: install `galley`, verify `claude`, `codex`, and `gh` availability, configure workflow roots, and prepare daemon/PR automation commands.
- **Troubleshooting**: inspect failed runs, stale claims, PR automation state, executor output, and recorded evidence.

```text
/galley:galley Create a Galley task for this feature request.
/galley:galley Create quality and environment profiles for this repository.
/galley:galley Diagnose this failed Galley run.
```

### Standalone Skill

For skills-compatible clients that do not use the Claude or Codex plugin manifests, copy or symlink this directory into the client's skill path:

```text
plugins/galley/skills/galley/
```

The standalone skill still expects the `galley` CLI on `PATH`. Some workflows also use `claude`, `codex`, or `gh`; the bundled helper scripts use `python3`.

## Core Concepts

- **Task YAML**: the trusted local input that defines the goal, acceptance criteria, scope, verification, execution policy, and PR behavior. See [docs/task-yaml.md](docs/task-yaml.md).
- **Quality profile**: optional repo-specific review gates, required checks, pass policy, and evidence requirements. See [docs/profiles.md](docs/profiles.md).
- **Environment profile**: optional repo-specific command map and execution constraints for network, secrets, and destructive operations. See [docs/profiles.md](docs/profiles.md).
- **AFK task**: an unattended task that can run asynchronously inside a managed worktree.
- **Acceptance criterion ID**: the `acceptance_criteria[].id` value Claude must report back with evidence, for example `AC1`.
- **Loop budget**: `execution_policy.loop_budget` is the maximum number of executor attempts before Galley escalates to supervisor review; `0` means unlimited.
- **Permission**: `scope.permission` sets the executor authority level for the task.
- **Input files**: optional `files[]` entries copy user-supplied files into the execution worktree with an explicit destination and commit policy.
- **Acceptance skeleton preflight**: optional `preflight.acceptance_skeleton` runs before the first executor attempt and writes acceptance-criterion-linked test skeletons into the worktree. Galley records the generated obligations, feeds them to the executor and supervisor, and requires matching quality-check evidence before accepting. See [docs/task-yaml.md](docs/task-yaml.md).
- **File-backed queue**: queued task copies move through `tasks/queued`, `tasks/running`, `tasks/done`, `tasks/failed`, and `tasks/archived`.
- **Worktree execution**: AFK tasks execute in a git worktree while `scope.cwd` continues to point at the source repository.
- **Structured evidence**: every attempt writes command plans, executor output, git status, diffs, and supervisor verdicts under `runs/`.
- **Supervisor review**: Galley sends structured run evidence to a model supervisor before accepting, retrying, or escalating.

`worktree.path` is resolved relative to `scope.cwd` and should point to a sibling directory outside the source repository, such as `../repo.worktrees/task-name`, so the source repository does not become dirty.

`files[].source` may be absolute or relative to the task YAML before queueing. `files[].destination` is a relative workspace path inside `scope.allowed_paths`; `commit: false` keeps context-only inputs out of the final commit or PR.

| Permission | Meaning |
| --- | --- |
| `read-only` | Read, inspect, and review only. |
| `edit` | Normal implementation work. File edits are allowed, but broad authority is not granted. |
| `sandbox-full-access` | Broad operations are allowed inside an isolated worktree or sandbox. |

## Task Lifecycle

```text
draft task YAML
        |
        | galley task queue
        v
tasks/queued/
        |
        | galley daemon claims without overwriting an existing running task
        v
tasks/running/
        |
        | execute Claude in worktree
        v
supervisor review
        |
        +-- accepted -----------------> tasks/done/accepted
        |
        +-- accepted + PR enabled ----> tasks/done/pr_opened
        |
        +-- needs_revision -----------> retry while loop budget remains
        |
        +-- needs_supervisor_review --> tasks/failed/
        |
        +-- hard_stop ----------------> tasks/failed/
```

Reviewed task files keep their terminal status in YAML. `tasks/done/` and `tasks/failed/` are flat directories; the `status` field distinguishes `accepted`, `pr_opened`, `needs_supervisor_review`, `failed`, `closed`, and `merged`. Reviewed task files can later be moved to `tasks/archived` with `galley task archive`. Archiving preserves the YAML audit trail instead of deleting state.

## Queue Layout

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

See [docs/profiles.md](docs/profiles.md) for supported profile fields and examples.

Each executor attempt writes evidence under `runs/<run-id>/attempt-N/`, where `<run-id>` is generated as `<task-id>-<unix-nano>`.

Attempt evidence includes:

- `command_plan.json`
- `run_result.json`
- `claude_result.json` when Claude returns valid structured JSON
- `supervisor_verdict.json`
- `git_status.json`
- `diff.patch`

Galley also records `workspace.json` for the effective execution workspace and writes `profiles.json` when quality or environment profiles are loaded.

## Commands

For hand-authored task YAML, use [docs/task-yaml.md](docs/task-yaml.md) as the reference.

The daemon root defaults to `~/.galley`, and `galley task queue` targets the running daemon root when one is available. Use `--root <path>` only for repo-local, test, or advanced multi-root workflows.

For one-shot local checks, use `galley daemon run --once` to drain the current queue and exit.

### Task Files

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

`galley task show` accepts a task file or task ID and prints the latest attempt, supervisor verdict, risk, and failed verification context. Once a task reaches an accepted terminal status (`accepted`, `pr_opened`, `closed`, or `merged`), the last attempt's claude status and error fields are relabeled under the `prior_attempt_*` prefix so they read as audit history rather than an active failure even after the daemon's PR cleanup loop transitions the task.

`galley task requeue` accepts a task ID or task file, returns a reviewed task from `tasks/failed`, `tasks/done`, or `tasks/running` to `tasks/queued`, records an optional reason, and increments `supervisor.review_iterations`.

### Profiles

```sh
galley profile validate --kind quality examples/quality-default.yaml
galley profile validate --kind environment examples/environment-local.yaml
galley profile resolve --cwd /path/to/repo --mkdir --output json
```

Quality and environment profiles are optional repository inputs. By default Galley resolves them from `~/.galley/profiles/<repo-key>/` using task `scope.cwd`. They add checks, constraints, preferred commands, and evidence requirements to the Claude work order.

The environment profile also owns repository operation defaults such as PR creation, PR comment handling, base branch, and worktree cleanup. The Galley skill can create profiles interactively by inspecting the repository and asking which checks, tools, network policy, secrets policy, PR behavior, and blocking severities apply.

### Schema References

```sh
galley schema check
galley schema generate
```

`galley schema generate` writes the task, quality profile, and environment profile schemas into the packaged skill references. `galley schema check` verifies those reference files still match the Go contracts and is run in CI.

### Daemon

```sh
galley daemon start
galley daemon status --output json
galley daemon stop
galley daemon run --once
```

`galley daemon run --once` drains queued tasks once and exits. Background `galley daemon start` also performs daemon maintenance such as PR comment polling and closed/merged PR worktree cleanup according to `environment.yaml`.

`--root` points at the daemon root and defaults to `~/.galley`. Use `--root .agent-workflow` only for repo-local or test workflows.

`galley daemon start` launches the daemon in the background. It writes a PID file and appends stdout/stderr to a log file. By default those files are under `~/.galley`; override them with `--pid-file` and `--log-file`.

`galley daemon stop` reads the PID file, sends `SIGTERM`, waits up to `--stop-timeout`, and removes the PID file when it still points at the stopped process. `galley daemon status` reports whether the PID file points at a live process.

`galley daemon stop --force` is an escape hatch for a stalled daemon. It:

1. Tries the normal graceful shutdown first.
2. Re-verifies process identity and sends `SIGKILL` when the daemon has not exited within `--stop-timeout`.
3. After the daemon is gone, sends `SIGKILL` to every registered executor and supervisor child process group recorded under the daemon root, waiting up to the same `--stop-timeout`.
4. If any child process group is still alive after that wait, prints an error naming the surviving PIDs and PGIDs and leaves the PID file in place so the operator can target the same daemon record.

A force kill can interrupt an active attempt; the next daemon startup recovers the interrupted running task (see Operational Notes).

Use the installed `galley` binary for `start`, `status`, and `stop`. PID verification records the executable path, so `go run ./cmd/galley ... daemon start` is not suitable for background daemon control because later `go run` invocations use different temporary binaries.

Foreground and background daemons use the same shutdown path. On `SIGINT` or `SIGTERM`, Galley stops claiming new queued tasks, lets active attempts finish until the shutdown timeout, records evidence, and avoids starting another retry attempt after shutdown is requested.

Each executor run and built-in model supervisor run is guarded by an idle-output watchdog. The default `--idle-timeout` is 10 minutes.

When a subprocess produces no stdout or stderr for that timeout, Galley terminates its process group, records a distinct idle-timeout result on the task attempt (`error_kind: idle_timeout`, `claude_status: idle_timed_out`) and in `runs/<run-id>/.../run_result.json` (`idle_timed_out: true`), and lets the loop continue according to the task loop budget instead of hanging on a stalled command. The watchdog is independent of the task's total per-attempt timeout, which still bounds total wall-clock duration. Raise `--idle-timeout` for executors that legitimately stay silent for long stretches while still making progress.

When the built-in model supervisor subprocess exits because of idle timeout, total timeout, or a forced subprocess kill, Galley retries the same supervisor evaluation up to two additional times inside the same executor attempt before failing the task. The retry budget is a fixed internal value, not a user-configurable knob: no new CLI flag, task YAML field, or profile field is added, and the executor attempt is not retried — only the supervisor evaluation is re-run, so the executor diff and run evidence from this attempt are preserved as-is. Each supervisor try writes its artifacts under `runs/<run-id>/attempt-N/supervisor-try-<M>/`, including `supervisor_error.json` for failed tries (with the classified `kind`) and `supervisor_verdict.json` for the successful try; the top-level `model_supervisor_verdict.json` is only written when one of the tries succeeds. If the retry budget is exhausted, the attempt records a supervisor-phase error with the classified `kind` (`idle_timeout`, `timed_out`, or `supervisor_failed`) under the original executor attempt directory and the task moves to `tasks/failed/` with status `needs_supervisor_review` so the retry evidence remains inspectable.

## Supervisor Behavior

Accepted tasks must:

- report every task acceptance criterion by ID
- mark every required acceptance criterion as `satisfied`
- include evidence for satisfied acceptance criteria
- report required quality checks as passed verification evidence
- return valid structured JSON matching the executor result schema

Otherwise the task is retried until `execution_policy.loop_budget` is exhausted; `loop_budget: 0` has no attempt cap.

| Condition | Result |
| --- | --- |
| `completed` with diff, satisfied ACs, evidence, and required checks | `tasks/done/` with status `accepted` |
| `completed` with executor-actionable implementation, scope, acceptance, or verification blockers | retry, then `tasks/failed/` with status `needs_supervisor_review` if the loop budget is exhausted |
| `completed` with blockers that require human product, design, business, environment setup, external service, or required-check policy judgment | `tasks/failed/` with status `needs_supervisor_review` |
| `completed` with an external or unrecoverable blocker that prevents meaningful progress | `tasks/failed/` with status `failed` |
| `completed_with_risks` | retry, then `tasks/failed/` with status `needs_supervisor_review` |
| Parse failure or schema validation failure | retry, then `tasks/failed/` with status `needs_supervisor_review` |
| Two consecutive attempts with no git diff | stop early as a no-progress safeguard |
| `hard_stop` | `tasks/failed/` with status `failed`, without retry |

For implementation tasks, the supervisor should reject a no-diff result unless the task is explicitly investigation or review-only and the evidence explains why no repository change was expected.

`needs_supervisor_review` is a task state, not a daemon process failure. `galley daemon run --once` can exit 0 after recording that state.

`completed_with_risks` means the executor believes the implementation is coherent, but verification limits, assumptions, or residual risks still need supervisor attention.

Hard-stop conditions are defined in the Claude executor prompt at `prompts/claude-executor-full.md`. In short, hard stops are reserved for blockers such as missing required secrets, inaccessible required systems, contradictory acceptance criteria, out-of-scope destructive actions, unreadable required files, or runtime failures that leave no useful next step.

## PR Automation

PR automation requires GitHub CLI (`gh`) to be installed and authenticated.

When `pr.enabled: true` is set in the environment profile, accepted worktree changes are committed with a Galley task commit message and `git_commit_result.json` is written to the run directory.

With `pr.enabled: true`, Galley pushes the current worktree branch to `origin`, writes `pr_body.md`, runs `gh pr create`, updates `pr.url` and `pr.status`, and moves the task to `done/pr_opened`. For AFK implementation tasks, PR creation plus comment polling is the recommended review loop. Local-only runs can leave PR automation disabled when GitHub credentials or network access are unavailable. The branch is the task YAML `worktree.branch`; the base branch comes from `pr.base`. Galley also branches each new AFK task worktree from `pr.base`: when an `origin` remote is configured the daemon refreshes the remote-tracking ref with `git fetch --no-tags --quiet origin <pr.base>` and uses `refs/remotes/origin/<pr.base>` as the start-point. If that fetch fails the daemon refuses to fall back to a possibly stale local `origin/<pr.base>` and fails the claimed task in the `workspace` phase so `galley task show` exposes the reason. Origin-less local checkouts fall back to `refs/heads/<pr.base>`. The start-point matches the eventual `gh pr create --base <pr.base>` target instead of inheriting whatever commit the source repository's HEAD currently points at.

Galley renders each acceptance criterion in `pr_body.md` using the supervisor verdict, so accepted ACs read as `Status: satisfied` and any IDs the supervisor flagged in `acceptance_gaps` read as `Status: partially_satisfied`. Generated PR titles preserve the task goal up to a rune budget set close to GitHub's 256-byte PR title limit, so ordinary long ASCII goals are not prematurely truncated. When truncation is still required, Galley cuts at the last whitespace inside that rune budget, enforces the 256-byte PR title limit on a valid UTF-8 boundary (so 4-byte runes such as emoji never overflow), and appends a single `…` so reviewers can tell the title was shortened.

With `pr.comments.enabled: true`, the daemon scans task files with `pr.url`, reads GitHub issue comments through `gh api`, and processes the oldest unprocessed Galley command for each task. A comment is accepted when its body, after trimming surrounding whitespace, starts with `/galley`. Recognised forms are:

- `/galley <free-form request>` — the text after the prefix becomes the request reason (for example `/galley fix the failing test`).
- `/galley rerun ...` and `/galley requeue ...` — backward-compatible aliases; the alias word is stripped so the parsed reason is the same as before.
- `/galley` alone — a no-arg requeue using the default reason.

Mid-line mentions like `Looks good, /galley rerun`, a `/galley` line that appears only after the first non-whitespace line, `/galley:galley ...`, and `/galleyfoo ...` are ignored. Processed comment IDs are stored in `pr.processed_comment_ids` so commands are not applied twice.

With `pr.comments.reply: true`, Galley posts a concise acknowledgement comment after handling a Galley command. The reply does not quote the original request body; the parsed request text is preserved on the requeued task as a `RevisionRequest` entry so the executor still receives the user's intent on the next attempt. Reply forms:

- Successful requeue: ``Galley requeued task `<task-id>` from this comment.``
- Comment received while the task is queued or running: `Galley noted this comment; task is already <status>.`
- Comment from a non-PR author: `Galley ignored this comment because only the pull request author can run Galley from PR comments.`

## Supervisors

Supervisor review defaults to Claude. Use `--supervisor codex` to select Codex instead, or `--supervisor claude` to be explicit. Repository-specific PR behavior, comment polling, and worktree cleanup live in the environment profile resolved from `scope.cwd`.

## Worktree Cleanup

With `worktree.cleanup: true`, the daemon scans `tasks/done` PR tasks and checks PR state through `gh api`.

- Open PRs keep their worktree.
- Closed or merged PRs remove the managed task worktree, including uncommitted or generated files left in that worktree.

Cleanup refuses to remove the source repository itself or one of its ancestor directories.

## Operational Notes

- Galley is intended for trusted local repositories and local filesystems. Queue claims rely on no-overwrite file creation plus atomic rename behavior on the same filesystem.
- Running multiple daemon processes is supported by claim conflict handling, but shared network filesystems may not provide the same rename and mtime behavior as a local disk.
- Background control uses a local PID file. Avoid sharing the same `--pid-file` across unrelated processes, and prefer one workflow root per managed daemon.
- Running tasks heartbeat their YAML mtime while the executor loop is active. `--heartbeat-interval` defaults to `min(claim-ttl/4, 1m)`.
- Each claimed running task records the owning daemon. On startup the daemon immediately requeues running tasks whose recorded owner is dead or cannot be verified — without waiting for `--claim-ttl` — while leaving tasks still owned by a verified live daemon untouched. A running task with no recorded owner (claimed by an older Galley, a claim recorded before its owner sidecar was written, or live work from a concurrent daemon that did not record ownership) is left for the mtime-based `--claim-ttl` recovery so freshly claimed live work is never requeued. Restart recovery can reuse an existing task worktree even when context-only input files are still present from a prior run; identical content is refreshed and conflicting content fails the claimed task with clear evidence. The mtime-based `--claim-ttl` recovery still runs each cycle as a backstop.
- Avoid very short `--claim-ttl` values. Filesystems with coarse mtime resolution can make overly aggressive stale-claim detection noisy.
- PR comment polling uses `gh api`; choose a polling interval that respects GitHub API rate limits for your account and repository count.
- Closed or merged PR cleanup treats the task worktree as disposable execution state. Keep anything that should survive cleanup outside the task worktree.
- Executor process failures and infrastructure errors are reported after the current queue is drained.

## Trust Model

Galley treats task YAML as trusted local execution input. A user or process that can write task YAML can choose the goal, scope, reference files, branch/worktree, and acceptance verification guidance used by the executor and supervisor.

Runnable quality checks come from profiles and are executed locally through `/bin/sh -c` inside the selected workspace.

PR comments can request requeueing and add instructions, but they do not rewrite profile checks. A PR comment is accepted as a Galley command only when the comment author login matches the PR author login recorded on the task; comments from any other GitHub user are ignored and (when `pr.comments.reply` is enabled) receive a concise rejection reply.

Run Galley only for repositories and task authors you trust. Keep secrets out of task-accessible files, and use worktrees, `scope.forbidden_paths`, executor sandboxing, and local OS controls to keep executor changes bounded. Treat `scope.allowed_paths` as the expected implementation area, not as the final PR staging boundary.

Automatic commit/PR creation stages the accepted final diff while blocking changes inside `scope.forbidden_paths`, so repository `.gitignore` should cover local editor, OS, cache, and secret files.

See [SECURITY.md](SECURITY.md) for reporting and operational trust boundaries.

## Development Examples

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

For local development and release notes, see [CONTRIBUTING.md](CONTRIBUTING.md) and [CHANGELOG.md](CHANGELOG.md).

Release assets are built by GitHub Actions when a GitHub Release is published. See [.github/workflows/release.yml](.github/workflows/release.yml) and [.goreleaser.yaml](.goreleaser.yaml).

## License

Galley is released under the MIT License. See [LICENSE](LICENSE).
