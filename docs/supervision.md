# Supervision

Galley separates implementation from acceptance. The executor produces work and structured evidence; the supervisor reviews that evidence against the task and repository policy.

## Executors

At every run start, Galley resolves each executor field independently:

1. explicit task `executor.cli` / `executor.model` / `executor.effort`
2. current repository `environment.yaml` `executor.default_cli` / `executor.model` / `executor.effort`
3. the built-in `cli: claude` default; an omitted effort or model lets the selected provider CLI choose its own reasoning effort and model

Environment values remain runtime-only, so requeues pick up current profile defaults. All executor roles use the same resolution, validated before invocation.

Galley supports Claude Code, Codex, GLM, and Grok. GLM uses the `claude` binary and `glm_api_key`; Grok uses its logged-in CLI state. Galley owns provider prompt transport.

See [task-yaml.md](task-yaml.md) for the full `executor` block and [../examples/afk-task-codex.yaml](../examples/afk-task-codex.yaml) for a Codex task example.

## Supervisors

Supervisor review defaults to Claude. Use `--supervisor codex` for Codex, `--supervisor glm` for GLM (the `claude` binary pointed at GLM's Z.ai endpoint; needs `glm_api_key` in `daemon.yaml`), or `--supervisor claude` to be explicit. The supervisor is the acceptance gate; the backend is your choice.

All supervisor backends use the same verdict contract, retry budget, and evidence layout. Repository-specific PR behavior, comment polling, and worktree cleanup live in the environment profile resolved from `scope.cwd`.

Supervisor selection controls only review. It is independent from the executor backend in `executor.cli`.

## Acceptance Requirements

Accepted tasks must:

- report every task acceptance criterion by ID
- mark every required acceptance criterion as `satisfied`
- include evidence for satisfied acceptance criteria
- report required quality checks as passed verification evidence
- return valid structured JSON matching the executor result schema

Otherwise the task is retried until `execution_policy.loop_budget` is exhausted; `loop_budget: 0` has no attempt cap.

For implementation tasks, the supervisor should reject a no-diff result unless the task is explicitly investigation or review-only and the evidence explains why no repository change was expected.

## Verdict Outcomes

| Condition | Result |
| --- | --- |
| `completed` with diff, satisfied ACs, evidence, and required checks | `tasks/done/` with status `accepted` |
| `completed` with executor-actionable implementation, scope, acceptance, or verification blockers | retry, then `tasks/failed/` with status `needs_supervisor_review` if the loop budget is exhausted |
| `completed` with blockers that require human judgment about product, design, environment, external services, or required-check policy | `tasks/failed/` with status `needs_supervisor_review` |
| `completed` with an external or unrecoverable blocker that prevents meaningful progress | `tasks/failed/` with status `failed` |
| `completed_with_risks` | retry, then `tasks/failed/` with status `needs_supervisor_review` |
| Parse or schema validation failure after a normal provider terminal | retry, then `tasks/failed/` with status `needs_supervisor_review` |
| Executor provider or runtime interruption (no normal provider terminal) | `tasks/failed/` with status `failed`, without supervisor review or retry |
| Two consecutive attempts with no git diff | stop early as a no-progress safeguard |
| `hard_stop` | `tasks/failed/` with status `failed`, without retry |

`needs_supervisor_review` is a task state, not a daemon process failure. `galley daemon run --once` can exit 0 after recording that state.

## Executor Interruptions

When an executor process ends, Galley first decides whether the attempt reached a normal provider terminal. The decision uses only the runner state and the provider's machine-readable completion marker — Claude and GLM successful result events, Codex `turn.completed`, and Grok `EndTurn` — and never the process exit code or error-text matching alone. A start failure, timeout, idle timeout, kill, explicit failed terminal (Codex `turn.failed`, Grok non-`EndTurn`, a Claude/GLM API-error result), or any output without a reliable normal terminal is an interruption.

An interrupted attempt stops before the supervisor: Galley does not invoke the supervisor and does not start another executor attempt in the same run, regardless of loop budget or whether the worktree has a diff. The task moves to `tasks/failed/` with status `failed` and the latest attempt records `supervisor_verdict: not_reviewed` and an executor-owned `error_kind: executor_interrupted`, distinct from an ordinary completed-result failure. Galley captures the command plan, run result, raw provider output, git status, diff, and an `executor_terminal.json` decision record before publishing the failed task, and preserves the worktree with its tracked and untracked changes.

`galley task show TASK_ID` surfaces the interruption, its evidence directory, and any retained provider detail (structured status, code, stop reason, session ID, or message; unavailable detail falls back to a generic reason). Resolve the interruption cause, then run `galley task requeue TASK_ID` to reuse the retained worktree and start a fresh executor attempt from the preserved changes.

`completed_with_risks` means the executor believes the implementation is coherent, but verification limits, assumptions, or residual risks still need supervisor attention.

Hard stops are reserved for blockers such as missing required secrets, inaccessible required systems, contradictory acceptance criteria, out-of-scope destructive actions, unreadable required files, or runtime failures that leave no useful next step.
