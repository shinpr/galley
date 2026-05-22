# Supervision

Galley separates implementation from acceptance. The executor produces work and structured evidence; the supervisor reviews that evidence against the task and repository policy.

## Executors

For newly authored tasks, Galley resolves the executor in this order:

1. an explicit executor choice during task authoring
2. `environment.yaml` `executor.default_cli`
3. Codex

The generated task records the selected backend in `executor.cli`. After that, the task YAML is authoritative: existing tasks keep their configured executor unless the task file is edited.

Galley supports Claude Code and Codex as executor backends. Acceptance skeleton preflight, structured executor results, run evidence, and supervisor review use the same contracts across both backends.

The executor backend in `executor.cli` also drives acceptance skeleton preflight: when `preflight.acceptance_skeleton.enabled` is true, the built-in skeleton creator runs through the same backend (and the same `executor.model`/`executor.effort` settings) as the implementation attempt. A Codex task creates skeletons with Codex; a Claude task creates them with Claude Code.

See [task-yaml.md](task-yaml.md) for the full `executor` block and [../examples/afk-task-codex.yaml](../examples/afk-task-codex.yaml) for a Codex task example.

## Supervisors

Supervisor review defaults to Codex. Use `--supervisor claude` to select Claude instead, or `--supervisor codex` to be explicit.

Both supervisor backends use the same verdict contract, retry budget, and evidence layout. Repository-specific PR behavior, comment polling, and worktree cleanup live in the environment profile resolved from `scope.cwd`.

Supervisor selection controls only review. It is independent from the executor backend in `executor.cli`, which drives the implementation attempt and acceptance skeleton preflight. A task can run a Codex executor and acceptance skeleton creator while a Claude supervisor reviews the result, or the reverse.

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
| Parse failure or schema validation failure | retry, then `tasks/failed/` with status `needs_supervisor_review` |
| Two consecutive attempts with no git diff | stop early as a no-progress safeguard |
| `hard_stop` | `tasks/failed/` with status `failed`, without retry |

`needs_supervisor_review` is a task state, not a daemon process failure. `galley daemon run --once` can exit 0 after recording that state.

`completed_with_risks` means the executor believes the implementation is coherent, but verification limits, assumptions, or residual risks still need supervisor attention.

Hard-stop conditions are defined in the executor prompts at [prompts/claude-executor-full.md](../prompts/claude-executor-full.md) and [prompts/codex-executor-full.md](../prompts/codex-executor-full.md). In short, hard stops are reserved for blockers such as missing required secrets, inaccessible required systems, contradictory acceptance criteria, out-of-scope destructive actions, unreadable required files, or runtime failures that leave no useful next step.
