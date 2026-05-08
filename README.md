# Galley

Galley is a local orchestration system for supervised agent task execution.

The first implementation slice provides:

- `galley task validate` for Galley task YAML validation.
- `galley task work-order` for rendering the Claude executor work order.
- `galley claude args` for previewing or exporting a Claude Code non-interactive invocation.
- `galley claude run` for running the exported invocation without a shell.
- `galleyd --once` for processing queued file-backed tasks.
- Codexized Claude executor, supervisor review, and corrective work-order prompts.
- A structured Claude executor result schema.
- HITL and AFK task examples.

## Development

```sh
go test ./...
go build ./cmd/galley
```

## Examples

```sh
go run ./cmd/galley task validate examples/hitl-task.yaml
go run ./cmd/galley task work-order examples/hitl-task.yaml
go run ./cmd/galley task queue .agent-workflow/tasks/ready/TASK.yaml --reason "ready for daemon"
go run ./cmd/galley task requeue .agent-workflow/tasks/failed/TASK.yaml --reason "addressed review feedback"
go run ./cmd/galley profile validate --kind quality examples/quality-default.yaml
go run ./cmd/galley profile validate --kind environment examples/environment-local.yaml
go run ./cmd/galley claude args examples/hitl-task.yaml
go run ./cmd/galley claude args examples/hitl-task.yaml --quality-profile-file examples/quality-default.yaml --environment-profile-file examples/environment-local.yaml
go run ./cmd/galley claude args examples/hitl-task.yaml --output json
go run ./cmd/galley claude run examples/hitl-task.yaml --stdout-file tmp/claude.stdout --stderr-file tmp/claude.stderr
go run ./cmd/galleyd --once --root .agent-workflow --manifest-file examples/repos.yaml
go run ./cmd/galleyd --once --root .agent-workflow --open-pr --pr-base main --quality-profile-file examples/quality-default.yaml --environment-profile-file examples/environment-local.yaml
go run ./cmd/galleyd --once --root .agent-workflow --poll-pr-comments --reply-pr-comments
./scripts/smoke-local.sh
```

Galley currently targets Claude Code `2.1.132`. That version supports `--system-prompt` and `--append-system-prompt`, but does not expose `--system-prompt-file`, `--append-system-prompt-file`, or `--max-turns` in `claude --help`.

`galley claude args --output json` returns an execution plan with `work_dir` and an argv array suitable for `exec.Command`; prompt and schema files are read by Go and passed as literal argument values. The default shell output is only a human preview and renders prompt/schema files as absolute-path shell `$(cat file)` substitutions before changing into the task cwd.

`galleyd` uses a file-backed queue:

```text
.agent-workflow/
  tasks/
    queued/
    running/
    done/
    failed/
  runs/
```

The first daemon slice claims queued YAML files with a no-overwrite lock plus atomic rename, creates or reuses the requested git worktree for AFK tasks, writes run evidence under `runs/<run-id>/`, updates task YAML status and attempt evidence, and moves tasks to `done` or `failed`. The task YAML keeps `scope.cwd` as the source repo; the effective execution workspace is recorded in `workspace.json` and the attempt summary. Dirty reused worktrees are recorded in `workspace.json` and as task risks. Claude's structured final JSON is extracted into each attempt's `claude_result.json` when available. Claim conflicts are skipped so another daemon or stale running file does not crash the process. `--claim-ttl` requeues stale `running/*.yaml` tasks and removes stale claim locks. `--once` drains the current queue using bounded batches controlled by `--max-concurrent-tasks`; executor process failures and infrastructure errors are returned after the queue is empty.

Each executor attempt writes evidence under `runs/<run-id>/attempt-N/`, including `command_plan.json`, `run_result.json`, `supervisor_verdict.json`, `git_status.json`, and `diff.patch`. The deterministic supervisor loop uses Claude's structured result plus git evidence to decide `accepted`, `needs_revision`, `needs_supervisor_review`, or `hard_stop`. It treats task YAML acceptance criteria as authoritative: every task AC ID must be reported by Claude with `satisfied` and evidence before the task can be accepted. `needs_revision` generates a corrective work order and reruns Claude until `execution_policy.loop_budget` is exhausted. `completed` with a non-empty git diff moves to `done/accepted`; `completed` with no diff, missing/unsatisfied ACs, `completed_with_risks`, parse failures, or contract validation failures are retried while budget remains and then move to `failed/needs_supervisor_review`. `hard_stop` moves to `failed/failed` without retry. `needs_supervisor_review` is a task state, not a daemon process failure; `galleyd --once` can exit 0 after recording that state.

Running tasks heartbeat their YAML mtime while the executor loop is active. `--heartbeat-interval` defaults to `min(claim-ttl/4, 1m)`, which prevents another daemon from treating an active long-running task as stale solely because the executor has not finished yet.

`galley task requeue` is the manual and future PR-comment bridge for another attempt. It changes a reviewed task back to `queued`, records an optional reason, increments `supervisor.review_iterations`, and moves files from `tasks/failed`, `tasks/done`, or `tasks/running` back into `tasks/queued` without overwriting an existing queued file.

Task authoring should produce `draft` or `ready` YAML. `galley task queue` is the deterministic handoff into execution: it validates the task, sets `status: queued`, records a queue attempt, and moves files from `tasks/ready` or `tasks/draft` into `tasks/queued` without overwriting an existing queued file. `galleyd` only claims files already in `tasks/queued`.

When `--poll-pr-comments` is enabled, `galleyd` scans `tasks/done` and `tasks/failed` for task YAML files with `pr.url`, reads GitHub issue comments through `gh api`, and processes `/galley rerun ...` or `/galley requeue ...` commands. Processed comment IDs are stored in `pr.processed_comment_ids` so rerun commands are not applied twice. A matching command requeues the task through the same no-overwrite state transition used by `galley task requeue`.

Quality and environment profiles are optional but first-class inputs. `galley profile validate` validates those YAML files, and `--quality-profile-file` / `--environment-profile-file` add their checks, constraints, preferred commands, and evidence requirements to the Claude work order. `galleyd` also writes the loaded profile bundle to `runs/<run-id>/profiles.json` for auditability.

`--manifest-file` loads repository defaults such as prompt/schema paths, profiles, PR policy, polling settings, and an optional external supervisor command. `--supervisor-command` can also be repeated on the CLI to provide an argv vector for an external supervisor. The external supervisor receives evidence JSON on stdin and must return a Galley supervisor verdict JSON on stdout.

When `--reply-pr-comments` is set with `--poll-pr-comments`, Galley posts an acknowledgement comment after handling a `/galley rerun` or `/galley requeue` command.

`scripts/smoke-local.sh` builds the binaries, creates a temporary git repository, installs a fake `claude` executable, queues a ready AFK task, runs `galleyd --once`, and verifies that the task reaches `done/accepted` with run evidence. Use it before replacing the fake executables with real Claude Code, Codex, and GitHub CLI integration.

When `--commit-on-accept` is set, accepted worktree changes are committed with a Galley task commit message and `git_commit_result.json` is written to the run directory. When `--open-pr` is set, Galley also pushes the worktree branch to `origin`, writes `pr_body.md`, runs `gh pr create`, updates `pr.url` / `pr.status`, and moves the task to `done/pr_opened`. PR creation is opt-in so local validation runs do not require GitHub credentials or network access.
