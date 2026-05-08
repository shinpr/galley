# Galley

Galley is a local orchestration runtime for supervised Claude Code task execution.

It is built for developers who want unattended AI-assisted repository work without giving up local control, git visibility, or reviewability.

Galley is Claude-first today. The executor path targets Claude Code, while the supervisor and queue layers are designed with extension points such as external supervisor commands, quality profiles, environment profiles, and repository manifests.

## Core Concepts

- **Task YAML**: the trusted local input that defines the goal, acceptance criteria, scope, verification, execution policy, and PR behavior.
- **AFK task**: an unattended task that can run asynchronously inside a managed worktree.
- **Acceptance criterion ID**: the `acceptance_criteria[].id` value Claude must report back with evidence, for example `AC1`.
- **Loop budget**: `execution_policy.loop_budget` is the maximum number of executor attempts before Galley escalates to supervisor review.
- **File-backed queue**: task files move through `tasks/queued`, `tasks/running`, `tasks/done`, `tasks/failed`, and `tasks/archived`.
- **Worktree execution**: AFK tasks execute in a git worktree while `scope.cwd` continues to point at the source repository.
- **Structured evidence**: every attempt writes command plans, executor output, git status, diffs, and supervisor verdicts under `runs/`.
- **Supervisor review**: Galley checks Claude's structured result against git state, acceptance criteria, and required checks before accepting, retrying, or escalating.

## Task Lifecycle

```text
draft task YAML
        |
        | galley task queue
        v
tasks/queued/
        |
        | galleyd claims with no-overwrite lock + atomic rename
        v
tasks/running/
        |
        | execute Claude in worktree
        v
supervisor review
        |
        +-- accepted -----------------> tasks/done/accepted
        |
        +-- accepted + --open-pr -----> tasks/done/pr_opened
        |
        +-- needs_revision -----------> retry while loop budget remains
        |
        +-- needs_supervisor_review --> tasks/failed/needs_supervisor_review
        |
        +-- hard_stop ----------------> tasks/failed/failed
```

Reviewed task files can later be moved to `tasks/archived` with `galley task archive`. Archiving preserves the YAML audit trail instead of deleting state.

## Queue Layout

```text
.agent-workflow/
  tasks/
    draft/
    queued/
    running/
    done/
    failed/
    archived/
  runs/
```

Each executor attempt writes evidence under `runs/<run-id>/attempt-N/`, where `<run-id>` is generated as `<task-id>-<unix-nano>`.

Attempt evidence includes:

- `command_plan.json`
- `run_result.json`
- `claude_result.json` when Claude returns valid structured JSON
- `supervisor_verdict.json`
- `git_status.json`
- `diff.patch`

Galley also records `workspace.json` for the effective execution workspace and writes `profiles.json` when quality or environment profiles are loaded.

## Quick Start

Build and test:

```sh
go test ./...
go build ./cmd/galley
```

Validate a task and inspect the work order:

```sh
go run ./cmd/galley task validate examples/hitl-task.yaml
go run ./cmd/galley task work-order examples/hitl-task.yaml
```

Queue a draft task and process the queue once:

```sh
go run ./cmd/galley task queue .agent-workflow/tasks/draft/TASK.yaml --reason "queue for daemon"
go run ./cmd/galleyd --once --root .agent-workflow --manifest-file examples/repos.yaml
```

Run the local smoke test:

```sh
./scripts/smoke-local.sh
```

The smoke test builds the binaries, creates a temporary git repository, installs a fake `claude` executable, queues a draft AFK task, runs `galleyd --once`, and verifies that the task reaches `done/accepted` with run evidence.

## Commands

### Task Files

```sh
go run ./cmd/galley task validate examples/hitl-task.yaml
go run ./cmd/galley task work-order examples/hitl-task.yaml
go run ./cmd/galley task queue .agent-workflow/tasks/draft/TASK.yaml --reason "queue for daemon"
go run ./cmd/galley task requeue .agent-workflow/tasks/failed/TASK.yaml --reason "addressed review feedback"
go run ./cmd/galley task archive .agent-workflow/tasks/done/TASK.yaml
```

`galley task queue` validates a `draft` task, sets `status: queued`, records a queue attempt, and moves it into `tasks/queued` without overwriting an existing queued file.

`galley task requeue` moves a reviewed task from `tasks/failed`, `tasks/done`, or `tasks/running` back into `tasks/queued`, records an optional reason, and increments `supervisor.review_iterations`.

### Profiles

```sh
go run ./cmd/galley profile validate --kind quality examples/quality-default.yaml
go run ./cmd/galley profile validate --kind environment examples/environment-local.yaml
```

Quality and environment profiles are optional inputs. They add checks, constraints, preferred commands, and evidence requirements to the Claude work order.

### Claude Invocation

```sh
go run ./cmd/galley claude args examples/hitl-task.yaml
go run ./cmd/galley claude args examples/hitl-task.yaml --output json
go run ./cmd/galley claude args examples/hitl-task.yaml --quality-profile-file examples/quality-default.yaml --environment-profile-file examples/environment-local.yaml
go run ./cmd/galley claude run examples/hitl-task.yaml --stdout-file tmp/claude.stdout --stderr-file tmp/claude.stderr
```

`galley claude args --output json` returns an execution plan with `work_dir` and an argv array suitable for `exec.Command`. Prompt and schema files are read by Go and passed as literal argument values.

The default shell output is a human preview. It renders prompt and schema files as absolute-path `$(cat file)` substitutions before changing into the task cwd.

### Daemon

```sh
go run ./cmd/galleyd --once --root .agent-workflow --manifest-file examples/repos.yaml
go run ./cmd/galleyd --once --root .agent-workflow --open-pr --pr-base main --quality-profile-file examples/quality-default.yaml --environment-profile-file examples/environment-local.yaml
go run ./cmd/galleyd --once --root .agent-workflow --poll-pr-comments --reply-pr-comments
go build -o ./bin/galleyd ./cmd/galleyd
./bin/galleyd --root .agent-workflow start
./bin/galleyd --root .agent-workflow status
./bin/galleyd --root .agent-workflow stop
```

`--root` points at the workflow directory and defaults to `.agent-workflow`.

`galleyd --once` processes the current queue in bounded concurrent batches. `--max-concurrent-per-repo` limits simultaneously running source repositories so local services, branch operations, and CI quotas are less likely to collide.

Without `--once`, `galleyd` runs continuously and checks for work every `--poll-interval`.

`galleyd start` launches the daemon in the background. It writes a PID file and appends stdout/stderr to a log file. By default those files are `.agent-workflow/galleyd.pid` and `.agent-workflow/galleyd.log`; override them with `--pid-file` and `--log-file`.

`galleyd stop` reads the PID file, sends `SIGTERM`, waits up to `--stop-timeout`, and removes the PID file when it still points at the stopped process. `galleyd status` reports whether the PID file points at a live process.

Use the same built `galleyd` binary for `start`, `status`, and `stop`. PID verification records the executable path, so `go run ./cmd/galleyd ... start` is not suitable for background daemon control because later `go run` invocations use different temporary binaries.

Foreground and background daemons use the same shutdown path. On `SIGINT` or `SIGTERM`, Galley stops claiming new queued tasks, lets active attempts finish until `--shutdown-timeout`, records evidence, and avoids starting another retry attempt after shutdown is requested.

## Supervisor Behavior

Accepted tasks must:

- produce a non-empty git diff
- report every task acceptance criterion by ID
- mark every required acceptance criterion as `satisfied`
- include evidence for satisfied acceptance criteria
- report required quality checks as passed verification evidence
- return valid structured JSON matching the executor result schema

Otherwise the task is retried until `execution_policy.loop_budget` is exhausted.

| Condition | Result |
| --- | --- |
| `completed` with diff, satisfied ACs, evidence, and required checks | `done/accepted` |
| `completed` with missing diff, missing ACs, unsatisfied ACs, or missing required checks | retry, then `failed/needs_supervisor_review` |
| `completed_with_risks` | retry, then `failed/needs_supervisor_review` |
| Parse failure or schema validation failure | retry, then `failed/needs_supervisor_review` |
| Two consecutive attempts with no git diff | stop early as a no-progress safeguard |
| `hard_stop` | `failed/failed` without retry |

`needs_supervisor_review` is a task state, not a daemon process failure. `galleyd --once` can exit 0 after recording that state.

`completed_with_risks` means the executor believes the implementation is coherent, but verification limits, assumptions, or residual risks still need supervisor attention.

Hard-stop conditions are defined in the Claude executor prompt at `prompts/claude-executor-full.md`. In short, hard stops are reserved for blockers such as missing required secrets, inaccessible required systems, contradictory acceptance criteria, out-of-scope destructive actions, unreadable required files, or runtime failures that leave no useful next step.

## PR Automation

PR automation requires GitHub CLI (`gh`) to be installed and authenticated.

When `--commit-on-accept` is set, accepted worktree changes are committed with a Galley task commit message and `git_commit_result.json` is written to the run directory.

When `--open-pr` is set, Galley pushes the current worktree branch to `origin`, writes `pr_body.md`, runs `gh pr create`, updates `pr.url` and `pr.status`, and moves the task to `done/pr_opened`. For AFK tasks, the branch is the task YAML `worktree.branch`. PR creation is opt-in so local validation runs do not require GitHub credentials or network access.

When `--poll-pr-comments` is enabled, `galleyd` scans task files with `pr.url`, reads GitHub issue comments through `gh api`, and processes the oldest unprocessed `/galley rerun ...` or `/galley requeue ...` command for each task. Processed comment IDs are stored in `pr.processed_comment_ids` so rerun commands are not applied twice.

When `--reply-pr-comments` is set with `--poll-pr-comments`, Galley posts an acknowledgement comment after handling a rerun or requeue command.

## Manifests And External Supervisors

`--manifest-file` loads repository defaults such as prompt/schema paths, profiles, PR policy, polling and cleanup settings, and an optional external supervisor command.

`--supervisor-command` can also be repeated on the CLI to provide an argv vector for an external supervisor. The external supervisor receives evidence JSON on stdin and must return a Galley supervisor verdict JSON on stdout.

## Worktree Cleanup

When `--cleanup-worktrees` is enabled, `galleyd` scans `tasks/done` PR tasks and checks PR state through `gh api`.

- Open PRs keep their worktree.
- Closed or merged PRs remove only clean git worktrees.
- Dirty worktrees are preserved and recorded as task risks for manual review.

This is intentionally conservative. A dirty worktree may contain useful work, failed recovery state, or files that should be inspected before removal.

## Claude Code Compatibility

Galley currently targets Claude Code `2.1.132`.

That version supports `--system-prompt` and `--append-system-prompt`, but does not expose `--system-prompt-file`, `--append-system-prompt-file`, or `--max-turns` in `claude --help`. For that reason Galley reads prompt and schema files itself and passes their contents through `--system-prompt` / `--append-system-prompt` and `--json-schema`.

If a task sets `executor.max_turns`, Galley records a warning because Claude Code `2.1.132` does not expose a matching flag.

## Operational Notes

- Galley is intended for trusted local repositories and local filesystems. Queue claims rely on no-overwrite file creation plus atomic rename behavior on the same filesystem.
- Running multiple `galleyd` processes is supported by claim conflict handling, but shared network filesystems may not provide the same rename and mtime behavior as a local disk.
- Background control uses a local PID file. Avoid sharing the same `--pid-file` across unrelated processes, and prefer one workflow root per managed daemon.
- Running tasks heartbeat their YAML mtime while the executor loop is active. `--heartbeat-interval` defaults to `min(claim-ttl/4, 1m)`.
- Avoid very short `--claim-ttl` values. Filesystems with coarse mtime resolution can make overly aggressive stale-claim detection noisy.
- PR comment polling uses `gh api`; choose a polling interval that respects GitHub API rate limits for your account and repository count.
- Dirty worktrees are not cleaned automatically. Review their recorded task risks before manual cleanup.
- Executor process failures and infrastructure errors are reported after the current queue is drained.

## Trust Model

Galley treats task YAML as trusted local execution input. A user or process that can write task YAML can choose `acceptance_criteria[].verification`, which Galley runs through `/bin/sh -c` inside the selected workspace, and can influence the branch/worktree used for execution.

PR comments can request requeueing and add instructions, but they do not rewrite verification commands.

Run Galley only for repositories and task authors you trust. Keep secrets out of task-accessible files, and use worktrees plus allowed paths to keep executor changes bounded.

Automatic commit/PR creation currently stages the accepted worktree state with `git add -A`, so repository `.gitignore` should cover local editor, OS, cache, and secret files.
