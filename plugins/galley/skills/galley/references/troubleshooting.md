# Troubleshooting

Use this reference when a Galley command fails, reports a warning or available update, or a task is failed, stale, accepted unexpectedly, stuck in running, or producing weak output.

## First Checks

For task and run problems:

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
| `setup_result.json` (run directory, not `attempt-N/`) | Setup phase result: attempted commands, source, readiness evidence, and repair guidance |
| `environment_update.json` (run directory, when present) | Audit trail for a learned setup plan persisted back to `environment.yaml` |

Read the files that exist. Focus on the newest attempt first.

## Diagnosis Patterns

| Symptom | Likely Cause | Action |
| --- | --- | --- |
| `galley update available: <current> -> <latest>` | A newer CLI release is available independently of the command result. | Use the exit status to determine command success. Report the current and latest versions and ask once whether to update and restore a verified running daemon. Continue the active flow from its first unfinished step if declined; after approval, switch to the Setup / daemon CLI update procedure. |
| task remains in `queued` | daemon stopped, concurrency limit, or queue mismatch | Check `galley daemon status`, daemon log, and the queued file path. |
| task remains in `running` | stale claim or active attempt | Check heartbeat age and claim TTL. |
| `needs_revision` loops | AC too broad, executor missing context, or supervisor finding unclear | Rewrite AC or add `revision_requests` with concrete next work. |
| task is `failed` after the executor stopped and no supervisor verdict was produced | executor execution was interrupted before review; provider evidence may identify a temporary limit or an incompatible executor setting | Continue with Executor interruption recovery below. |
| `accepted` with quality concerns | pass policy treats concern as non-blocking | Add quality profile or PR comment requeue with specific blocker. |
| no diff produced | task was investigation-only or executor stopped early | Check executor result and work order. |
| PR comments ignored | polling disabled in `environment.yaml`, auth missing, or comment ID already processed | Check `pr.comments.enabled`, `gh auth status`, and `pr.processed_comment_ids`. |
| attempt kind is `setup_failed` | setup executor could not make the worktree ready from `environment.setup`, `environment.commands`, or repository signals | Read `setup_result.json` plus `setup_executor.stderr.log` / `setup_executor.stdout.jsonl`; fix `environment.yaml setup.commands` or remove the `setup` field. See Setup failures below. |

## Repair Actions

When a failed task needs a task-changing human instruction, requeue it with:

```bash
galley task requeue <task-id-or-task-file> --revision-request "<concrete next work>"
```

Use the operational recovery commands below when the task contract does not change.

### Executor interruption recovery

When a task is `failed` because the executor stopped before supervisor review, continue from its retained worktree and diff. Use observed evidence to choose the recovery action:

- Update task-level executor overrides when the error identifies the current CLI, model, or effort as incompatible, or when the user chooses different settings.
- Otherwise keep the same settings; when the observed condition is temporary, wait before requeueing.

Provider-specific error details improve `Cause`, `Evidence`, and the requeue reason; an exact provider failure classification is optional. Record the observed symptom and only an established cause; when the cause is not established, use `cause unknown`.

For the same executor settings, inspect the task and requeue:

```bash
galley task show <task-id>
galley task requeue <task-id-or-task-file> --reason "retry after executor interruption: <observed symptom>; cause <established cause or unknown>"
```

To change task-level executor overrides, run exactly one helper command with only the fields that should change. An `--unset-*` option removes that task override so runtime defaults apply. The helper changes `executor.cli`, `executor.model`, and `executor.effort`; it leaves the recovery decision to this flow.

Use the Python launcher available on the host (`python3`, `python`, or Windows `py -3`); these examples use `python3`.

To set task overrides:

```bash
python3 <this-skill-directory>/scripts/update_task_executor.py <task-file> --cli <cli> --model <model> --effort <effort>
```

To remove selected task overrides:

```bash
python3 <this-skill-directory>/scripts/update_task_executor.py <task-file> --unset-model --unset-effort
```

Proceed to validation only when the selected helper command exits 0. When it exits non-zero, report its error and correct the reported file or YAML problem before rerunning the helper.

```bash
galley task validate <task-file>
```

When validation exits 0, requeue:

```bash
galley task requeue <task-id-or-task-file> --reason "retry with updated executor settings: <observed symptom>; cause <established cause or unknown>"
```

For a weak supervisor acceptance:

1. Convert the concern into a concrete PR comment or `revision_request`.
2. State the wrong behavior scenario, file path, and expected fix.
3. Requeue the task.

For environment failure:

1. Record the missing command, secret, service, or permission.
2. Fix setup or mark as `needs_supervisor_review` if human action is required.

## Setup failures (phase=setup, kind=setup_failed)

A `setup_failed` attempt means Galley could not make the task worktree ready before the implementation executor ran. Setup evidence lives in the run directory (`~/.galley/runs/<task-id>/<run-id>/`), not under `attempt-N/`.

- `setup_result.json` — source-of-truth setup evidence. Read these fields first:
  - `status`: should be `failed` for a `setup_failed` attempt. A `ready` value here indicates daemon/executor disagreement; escalate with the run evidence.
  - `commands[]`: every command the setup executor attempted, with `source` (`environment_setup`, `environment_commands`, or `discovered`), `exit_code`, and stdout/stderr excerpts.
  - `source`: which command list produced the final plan. Use `environment_setup` for a prior setup plan, `environment_commands` for a plan reused from `environment.commands`, and `discovered` for a plan composed from repository signals or conventions.
  - `inspected_files[]`: repository signals the setup executor read (manifests, lockfiles, setup docs).
  - `repair_guidance`: actionable guidance for fixing `environment.setup` or rerunning discovery.
- Matching full-output logs:
  - `setup_executor.stdout.jsonl` / `setup_executor.stderr.log` — setup executor invocation; stdout is the provider JSONL stream.
- `environment_update.json` — present only when Galley persisted or attempted to persist a learned setup plan back to `environment.yaml`. Audit fields: `profile_path`, `changed` (true when a setup plan was written; false when an update record exists but no profile change was published), `before` (prior setup plan or null), `after` (new plan), `diff`, `reason`, and `updated_at`. `diff` is a text representation of the setup command change and may be empty when no change was published. When a previously learned plan later fails, compare `before`/`after` to confirm which commands changed.

Repair flow:

1. Read `setup_result.json` and identify the failing command (the last attempt with non-zero `exit_code`).
2. Inspect `setup_executor.stderr.log` and `setup_executor.stdout.jsonl`; use `commands[].source` to tell whether the failing command came from `environment.setup`, `environment.commands`, or discovery.
3. Edit `environment.yaml setup.commands` when the failing command needs a small fix such as a typo, missing flag, or stale script name and the rest of the plan is still valid. Remove the `setup` field when the failure looks structural, such as a changed package manager, renamed setup flow, or toolchain migration, and rediscovery is cheaper than triage.
4. Requeue the task.

```bash
galley task requeue <task-id-or-task-file> --reason "retry after repairing environment.setup"
```

For setup failures, populate `Evidence:` in the report with the failing `setup_result.json` command, the matching stderr log path, and `environment_update.json` before/after or `diff` when a learned plan changed.

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
