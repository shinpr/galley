# Troubleshooting

Use this reference when a task is failed, stale, accepted unexpectedly, stuck in running, or producing weak output.

## First Checks

```bash
galley daemon status --output json
galley task list
galley task list --state failed
galley task show <task-id-or-task-file>
find ~/.galley/runs -maxdepth 3 -type f | sort
```

Identify:

- task YAML path
- task state and status
- latest `run_id`
- latest `attempt-N`
- executor status
- supervisor verdict
- latest risk or failed verification
- PR URL, if any

Use `galley task list` to find tasks by state. Use `galley task show` to inspect the selected task's latest attempt, supervisor verdict, summary, latest risk, and failed verification output.

## Evidence Files

Inspect the latest attempt directory:

| File | Purpose |
| --- | --- |
| `work_order.md` | Prompt and task context passed to executor |
| `input_files.json` | Input files copied into the worktree and their commit policy |
| `executor_result.json` | Structured executor output |
| `supervisor_verdict.json` | Model supervisor decision |
| `diff.patch` | Code changes reviewed by the supervisor |
| `command_plan.json` | Claude executor invocation plan |
| `codex_supervisor_request.json` | Evidence sent to Codex supervisor |
| `claude_supervisor_request.json` | Evidence sent to Claude supervisor |

Read the files that exist. Focus on the newest attempt first.

## Diagnosis Patterns

| Symptom | Likely Cause | Action |
| --- | --- | --- |
| task remains in `queued` | daemon stopped, concurrency limit, or queue mismatch | Check `galley daemon status`, daemon log, and the queued file path. |
| task remains in `running` | stale claim or active attempt | Check heartbeat age and claim TTL. |
| `needs_revision` loops | AC too broad, executor missing context, or supervisor finding unclear | Rewrite AC or add `revision_requests` with concrete next work. |
| task failed due to usage limit or transient provider failure | executor stopped for a temporary external limit | Wait until the limit resets, then requeue with a reason. |
| `accepted` with quality concerns | pass policy treats concern as non-blocking | Add quality profile or PR comment requeue with specific blocker. |
| no diff produced | task was investigation-only or executor stopped early | Check executor result and work order. |
| PR comments ignored | polling disabled in `environment.yaml`, auth missing, or comment ID already processed | Check `pr.comments.enabled`, `gh auth status`, and `pr.processed_comment_ids`. |

## Repair Actions

For a failed task with a clear next step:

1. Add or update `revision_requests` in the task YAML.
2. Reset unresolved AC statuses to `pending` where needed.
3. Requeue through Galley CLI.

```bash
galley task requeue <task-id-or-task-file> --reason "retry after fixing the blocker"
```

For usage limits or temporary provider failures:

```bash
galley task show <task-id>
galley task requeue <task-id-or-task-file> --reason "retry after usage limit reset"
```

For a weak supervisor acceptance:

1. Convert the concern into a concrete PR comment or `revision_request`.
2. State the wrong behavior scenario, file path, and expected fix.
3. Requeue the task.

For environment failure:

1. Record the missing command, secret, service, or permission.
2. Fix setup or mark as `needs_supervisor_review` if human action is required.

## Setup failures (phase=setup, kind=setup_failed)

A `setup_failed` attempt means Galley could not make the task worktree ready before the implementation executor ran. Inspect the run directory:

- `runs/<run-id>/setup_result.json` — source-of-truth setup evidence. Read these fields first:
  - `status`: `ready` or `failed`.
  - `commands[]`: every command Galley attempted, with `source` (`environment_setup`, `environment_commands`, `readiness_check`, or `discovered`), `exit_code`, and stdout/stderr excerpts. The full output remains in the per-command `setup_authored.N.{stdout,stderr}.log`, `setup_readiness_check.{stdout,stderr}.log`, or `setup_executor.{stdout.jsonl,stderr.log}` files.
  - `source`: which command list produced the final attempt (`environment_setup` for an authored plan, `discovered` for a learned plan).
  - `inspected_files[]`: repository signals the setup executor read (manifests, lockfiles, setup docs).
  - `repair_guidance`: actionable guidance for fixing `environment.setup` or rerunning discovery.
- `runs/<run-id>/environment_update.json` — present only when Galley persisted a learned setup plan back to `environment.yaml`. Audit fields: `profile_path`, `before` (prior setup plan or null), `after` (new plan), `reason`, and `updated_at`. When a previously learned plan later fails, compare `before`/`after` to confirm which commands changed and decide whether to repair `environment.setup` by hand or to delete the `setup` field and let Galley rediscover.

Repair flow:

1. Read `setup_result.json` and identify the failing command (the last attempt with non-zero `exit_code`).
2. Inspect the matching `setup_authored.N.stderr.log` or `setup_executor.stderr.log`.
3. Edit `environment.yaml setup.commands` to fix the command, or remove the `setup` field entirely to let Galley discover and persist a new plan on the next run.
4. Requeue the task.

## Report Format

```markdown
Status: <task status>
Run: <run id> / attempt <n>
Verdict: <supervisor verdict>
Cause: <one sentence>
Evidence:
- <file>: <fact>
Recommended action:
- <command or task edit>
```
