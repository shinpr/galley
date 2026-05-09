![Galley blueprint](docs/assets/galley-blueprint.jpg)

# Galley

Galley is a local orchestration runtime for supervised Claude Code task execution.

It runs locally, keeps work in git-visible changes, and records evidence for review before accepting unattended AI-assisted repository work.

Galley is Claude-first today. The executor path targets Claude Code, and supervisor review defaults to Claude. Codex can be selected as an alternate model supervisor.

Status: early preview.

## Install

Galley is intended to run from any repository you are working in, so install the `galley` binary on your `PATH`.

For guided setup, install the plugin and ask the Galley skill to create profiles, task YAML, and queueing commands for your repository. See [Plugin And Skill](#plugin-and-skill).

Install the latest GitHub Release binary:

```sh
curl -fsSL https://raw.githubusercontent.com/shinpr/galley/main/scripts/install.sh | sh
```

From a cloned checkout:

```sh
./scripts/install.sh --local
```

The cloned checkout path lets you inspect the installer before running it. The `curl` form executes the script fetched from GitHub and downloads the matching release asset for your OS and architecture.

Or use Go directly:

```sh
go install github.com/shinpr/galley/cmd/galley@latest
```

The installer installs the `galley` CLI. Queue processing and background daemon control are available under `galley daemon ...`.

```sh
galley --help
galley daemon run --once
galley daemon start
galley daemon status --output json
galley daemon stop
```

## Plugin And Skill

Galley includes a plugin that packages one Agent Skill for Claude Code and Codex. This is the easiest setup path: install the plugin, ask the skill to inspect your repository, and let it draft profiles, task YAML, validation steps, queueing commands, and troubleshooting guidance.

Profiles are worth setting up early. They tell Galley which commands are available, which quality checks are required, what evidence the supervisor should expect, and which findings should block acceptance. The skill can create these interactively from the target repository.

Plugin files:

```text
plugins/galley/
  .claude-plugin/plugin.json
  .codex-plugin/plugin.json
  skills/galley/
    SKILL.md
    references/
    scripts/
```

### Claude Code

Install from the GitHub-hosted marketplace:

```text
/plugin marketplace add shinpr/galley
/plugin install galley@galley-tools
/reload-plugins
```

Then invoke the skill:

```text
/galley:galley Create a Galley task for this feature request.
/galley:galley Create quality and environment profiles for this repository.
/galley:galley Diagnose this failed Galley run.
```

For local development, validate the plugin and marketplace from a checkout:

```sh
claude plugin validate plugins/galley
claude plugin validate .
```

You can also add the checkout as a local marketplace for testing:

```text
/plugin marketplace add .
/plugin install galley@galley-tools
/reload-plugins
```

### Codex

Galley ships a Codex marketplace file at `.agents/plugins/marketplace.json`, which points to `./plugins/galley`. Install it from the GitHub repository:

```sh
codex plugin marketplace add shinpr/galley
```

For local development:

```sh
codex plugin marketplace add .
```

The Codex CLI version used during development exposes marketplace `add`, `upgrade`, and `remove`, but no `plugin validate` or separate `plugin install` command. Validate the bundled skill with the local skill validator when available.

Invoke the skill from Codex with `$galley`, for example:

```text
$galley Create a validated Galley task and queue it after approval.
```

### Standalone Skill

For skills-compatible clients that do not use the Claude or Codex plugin manifests, copy or symlink this directory into the client's skill path:

```text
plugins/galley/skills/galley/
```

The standalone skill still expects the `galley` CLI on `PATH`. Some workflows also use `claude`, `codex`, or `gh`; the bundled helper scripts use `python3`.

### Skill Use Cases

- **Task authoring**: clarify the user goal, target repo, scope, input files, acceptance criteria, verification, loop budget, supervisor, and PR behavior; write a draft task YAML, validate it, and queue it only after explicit user approval.
- **Profile authoring**: interactively create `quality.yaml` and `environment.yaml` for repo-specific quality gates, commands, tools, network policy, secrets policy, PR behavior, and cleanup policy.
- **Setup**: install `galley`, check `claude`, `codex`, `gh`, configure workflow roots, and prepare daemon/PR automation commands.
- **Troubleshooting**: inspect failed runs, stale claims, PR automation state, executor output, and recorded evidence.

## Core Concepts

- **Task YAML**: the trusted local input that defines the goal, acceptance criteria, scope, verification, execution policy, and PR behavior. See [docs/task-yaml.md](docs/task-yaml.md).
- **Quality profile**: optional repo-specific review gates, required checks, pass policy, and evidence requirements. See [docs/profiles.md](docs/profiles.md).
- **Environment profile**: optional repo-specific command map and execution constraints for network, secrets, and destructive operations. See [docs/profiles.md](docs/profiles.md).
- **AFK task**: an unattended task that can run asynchronously inside a managed worktree.
- **Acceptance criterion ID**: the `acceptance_criteria[].id` value Claude must report back with evidence, for example `AC1`.
- **Loop budget**: `execution_policy.loop_budget` is the maximum number of executor attempts before Galley escalates to supervisor review; `0` means unlimited.
- **Permission**: `scope.permission` sets the executor authority level for the task.
- **Input files**: optional `files[]` entries copy user-supplied files into the execution worktree with an explicit destination and commit policy.
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

## Quick Start

Install the CLI, then use the plugin skill to create repository profiles and a draft task:

```sh
./scripts/install.sh
galley profile resolve --cwd /path/to/repo --mkdir --output json
```

After the skill writes and validates a draft task, approve queueing and process the queue:

```sh
galley task queue ./TASK.yaml --reason "queue for daemon"
galley daemon run --once
```

The daemon root defaults to `~/.galley`, and `galley task queue` targets the running daemon root when one is available. Use `--root <path>` only for repo-local, test, or advanced multi-root workflows. Use `--move` only when the source draft should be removed after queueing.

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

## Commands

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

`galley task show` accepts a task file or task ID and prints the latest attempt, supervisor verdict, risk, and failed verification context.

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

### Claude Invocation

```sh
galley claude args ~/.galley/tasks/draft/TASK.yaml
galley claude args ~/.galley/tasks/draft/TASK.yaml --output json
galley claude args ~/.galley/tasks/draft/TASK.yaml --quality-profile-file /path/to/quality.yaml --environment-profile-file /path/to/environment.yaml
```

`galley claude args --output json` returns an execution plan with `work_dir` and an argv array suitable for `exec.Command`. The default executor prompt and result schema are embedded in the `galley` binary. If `--system-prompt-file` or `--json-schema-file` is set, Galley reads that file and passes its contents as a literal Claude argument.

The default shell output is a human preview. Embedded defaults are shown as literal argument values; explicit prompt/schema files are rendered as absolute-path `$(cat file)` substitutions before changing into the task cwd.

### Daemon

```sh
galley daemon run --once
galley daemon run --once --supervisor codex
galley daemon start
galley daemon status --output json
galley daemon stop
```

`galley daemon run --once` drains queued tasks once and exits. Background `galley daemon start` also performs daemon maintenance such as PR comment polling and closed/merged PR worktree cleanup according to `environment.yaml`.

`--root` points at the daemon root and defaults to `~/.galley`. Use `--root .agent-workflow` only for repo-local or test workflows.

`galley daemon run --once` processes the current queue in bounded concurrent batches. `--max-concurrent-per-repo` limits simultaneously running source repositories so local services, branch operations, and CI quotas are less likely to collide.

Without `--once`, `galley daemon run` runs continuously and checks for work every `--poll-interval`, which defaults to `10s`.

`galley daemon start` launches the daemon in the background. It writes a PID file and appends stdout/stderr to a log file. By default those files are under `~/.galley`; override them with `--pid-file` and `--log-file`.

`galley daemon stop` reads the PID file, sends `SIGTERM`, waits up to `--stop-timeout`, and removes the PID file when it still points at the stopped process. `galley daemon status` reports whether the PID file points at a live process.

Use the installed `galley` binary for `start`, `status`, and `stop`. PID verification records the executable path, so `go run ./cmd/galley ... daemon start` is not suitable for background daemon control because later `go run` invocations use different temporary binaries.

Foreground and background daemons use the same shutdown path. On `SIGINT` or `SIGTERM`, Galley stops claiming new queued tasks, lets active attempts finish until `--shutdown-timeout`, which defaults to `5m`, records evidence, and avoids starting another retry attempt after shutdown is requested.

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
| `completed` with missing ACs, unsatisfied ACs, or missing required checks | retry, then `tasks/failed/` with status `needs_supervisor_review` |
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

With `pr.enabled: true`, Galley pushes the current worktree branch to `origin`, writes `pr_body.md`, runs `gh pr create`, updates `pr.url` and `pr.status`, and moves the task to `done/pr_opened`. For AFK implementation tasks, PR creation plus comment polling is the recommended review loop. Local-only runs can leave PR automation disabled when GitHub credentials or network access are unavailable. The branch is the task YAML `worktree.branch`; the base branch comes from `pr.base`.

With `pr.comments.enabled: true`, the daemon scans task files with `pr.url`, reads GitHub issue comments through `gh api`, and processes the oldest unprocessed `/galley rerun ...` or `/galley requeue ...` command for each task. Processed comment IDs are stored in `pr.processed_comment_ids` so rerun commands are not applied twice.

With `pr.comments.reply: true`, Galley posts an acknowledgement comment after handling a rerun or requeue command.

## Supervisors

Supervisor review defaults to Claude. Use `--supervisor codex` to select Codex instead, or `--supervisor claude` to be explicit. Repository-specific PR behavior, comment polling, and worktree cleanup live in the environment profile resolved from `scope.cwd`.

## Development Examples

The `examples/` directory is for Galley checkout development and CI validation. These files are useful for smoke tests and command previews, but normal users should prefer `~/.galley` tasks created by the plugin skill.

```sh
galley task validate examples/afk-task.yaml
galley task work-order examples/afk-task.yaml
galley daemon run --once
galley claude args examples/afk-task.yaml --output json
```

For local development and release notes, see [CONTRIBUTING.md](CONTRIBUTING.md) and [CHANGELOG.md](CHANGELOG.md).

Release assets are built by GitHub Actions when a GitHub Release is published. See [.github/workflows/release.yml](.github/workflows/release.yml) and [.goreleaser.yaml](.goreleaser.yaml).

## Worktree Cleanup

With `worktree.cleanup: true`, the daemon scans `tasks/done` PR tasks and checks PR state through `gh api`.

- Open PRs keep their worktree.
- Closed or merged PRs remove only clean git worktrees.
- Dirty worktrees are preserved and recorded as task risks for manual review.

This is intentionally conservative. A dirty worktree may contain useful work, failed recovery state, or files that should be inspected before removal.

## Claude Code Compatibility

Galley currently targets Claude Code `2.1.132`.

That version supports `--system-prompt` and `--append-system-prompt`, but does not expose `--system-prompt-file`, `--append-system-prompt-file`, or `--max-turns` in `claude --help`. For that reason Galley reads prompt and schema files itself and passes their contents through `--system-prompt` / `--append-system-prompt` and `--json-schema`.

## Operational Notes

- Galley is intended for trusted local repositories and local filesystems. Queue claims rely on no-overwrite file creation plus atomic rename behavior on the same filesystem.
- Running multiple daemon processes is supported by claim conflict handling, but shared network filesystems may not provide the same rename and mtime behavior as a local disk.
- Background control uses a local PID file. Avoid sharing the same `--pid-file` across unrelated processes, and prefer one workflow root per managed daemon.
- Running tasks heartbeat their YAML mtime while the executor loop is active. `--heartbeat-interval` defaults to `min(claim-ttl/4, 1m)`.
- Avoid very short `--claim-ttl` values. Filesystems with coarse mtime resolution can make overly aggressive stale-claim detection noisy.
- PR comment polling uses `gh api`; choose a polling interval that respects GitHub API rate limits for your account and repository count.
- Dirty worktrees are not cleaned automatically. Review their recorded task risks before manual cleanup.
- Executor process failures and infrastructure errors are reported after the current queue is drained.

## Trust Model

Galley treats task YAML as trusted local execution input. A user or process that can write task YAML can choose the goal, scope, reference files, branch/worktree, and acceptance verification guidance used by the executor and supervisor.

Runnable quality checks come from profiles and are executed locally through `/bin/sh -c` inside the selected workspace.

PR comments can request requeueing and add instructions, but they do not rewrite profile checks.

Run Galley only for repositories and task authors you trust. Keep secrets out of task-accessible files, and use worktrees plus allowed paths to keep executor changes bounded.

Automatic commit/PR creation currently stages the accepted worktree state with `git add -A`, so repository `.gitignore` should cover local editor, OS, cache, and secret files.

See [SECURITY.md](SECURITY.md) for reporting and operational trust boundaries.

## License

Galley is released under the MIT License. See [LICENSE](LICENSE).
