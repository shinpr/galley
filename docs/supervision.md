# Models and Supervision

Galley separates implementation from acceptance. The executor changes the repository; the supervisor reviews the result against the task and repository policy. You choose both roles explicitly.

Galley does not route tasks to models automatically. This keeps cost, capability, and acceptance authority visible while still allowing different model combinations for different repositories and tasks.

## Choosing a Model Setup

Use the combination that fits the work and the level of review you need.

| Pattern | When it fits |
| --- | --- |
| Same backend for executor and supervisor | A simple default when one provider is already trusted for the repository. |
| Faster or lower-cost executor with a stronger supervisor | Routine implementation where review quality matters more than using the strongest model for every attempt. |
| Specialized executor with an independent supervisor | Tasks that benefit from a provider's tooling, context, or coding behavior while retaining a separate acceptance check. |

The supervisor is not a substitute for suitable acceptance criteria or repository policy. A weaker executor can be useful when the task is bounded and the checks are strong; vague work with weak evidence remains difficult to supervise regardless of model choice.

## Supported Backends

Executors and supervisors support `claude`, `codex`, `glm`, and `grok`.

- Claude uses the `claude` CLI.
- Codex uses the `codex` CLI.
- GLM uses the `claude` CLI with the Z.ai endpoint and requires `glm_api_key` in `daemon.yaml`.
- Grok uses the logged-in `grok` CLI state.

All backends use Galley-owned prompt transport, result contracts, and evidence layout.

## Executor Configuration

Galley resolves `cli`, `model`, and `effort` independently at the start of every run:

1. the task's `executor` field
2. the repository `environment.yaml` executor defaults
3. the built-in `cli: claude` fallback

There is no built-in model or effort default. When both the task and environment profile omit one of those fields, the selected provider CLI uses its own configured default.

Task overrides are useful for a single job. Environment defaults define the repository's normal implementation setup. A requeued task picks up repository-level model changes because Galley reads the profile at the start of each run.

See [Task YAML](task-yaml.md#execution-policy-and-executor) for task overrides, [Profiles](profiles.md#environmentyaml) for repository defaults, and the [Codex task example](../examples/afk-task-codex.yaml) for a complete file.

## Supervisor Configuration

The supervisor backend resolves in this order:

1. repository `environment.yaml` `supervisor.default_cli`
2. the daemon startup `--supervisor` flag
3. `supervisor` in `daemon.yaml`
4. the built-in `claude` default

The repository environment profile also supplies the supervisor model and effort. `runs/<run-id>/supervisor.json` records the resolved backend, model, effort, and the source of each setting.

Executor and supervisor choices are independent. Changing the task executor does not change the supervisor.

## How Review Converges

The first supervisor attempt reviews every acceptance criterion before reviewing every configured quality dimension. This ordering keeps task obligations primary while still applying repository-wide standards.

Galley persists verified passes in daemon-owned task state. Later attempts review unfinished items and revisit a verified pass when the executor reports a current-attempt change that may affect it. The next executor receives the active revision work rather than an unstructured history of every earlier review.

A normal requeue preserves review progress when the task direction and review contract remain the same. Galley resets stale passes when the goal, acceptance contract, review policy, source repository, or placed input content changes in a way that invalidates earlier review.

## Acceptance Requirements

An accepted implementation task needs:

- a normal executor terminal with valid structured output
- a repository diff unless the task is explicitly investigation or review-only
- satisfied required acceptance criteria with evidence
- passing evidence for required checks
- all required quality dimensions and the configured weighted score
- no unresolved finding at a blocking severity

Incomplete work can continue while the loop budget remains when the supervisor returns actionable revision work. Human decisions, exhausted budgets, supervisor failures, and finalization problems move the task to `needs_supervisor_review`. A hard stop or executor interruption ends the run without another attempt.

`completed_with_risks` means the executor considers the implementation coherent but has verification limits, assumptions, or residual risks for the supervisor to evaluate.

## Outcomes

| Outcome | What Galley does |
| --- | --- |
| Accepted | Moves the task to `done/accepted`, then performs configured PR handoff. |
| Executor-actionable revision | Starts another executor attempt while the loop budget remains. |
| Human decision or exhausted loop | Moves the task to `failed/needs_supervisor_review` with the latest evidence and work order. |
| Supervisor process or verdict failure | Preserves supervisor artifacts and moves the task to `needs_supervisor_review`. |
| Hard stop | Moves the task to `failed/failed` without retry. |
| Executor interruption before a normal terminal | Preserves the worktree and evidence, skips supervisor review, and moves the task to `failed/failed`. |
| Two consecutive attempts with no diff | Stops early as a no-progress safeguard. |

`needs_supervisor_review` is a task state, not a daemon process failure. `galley daemon run --once` can exit successfully after recording it.

Hard stops are reserved for conditions that leave no useful executor action, such as missing required secrets, inaccessible required systems, contradictory acceptance criteria, protected destructive actions, or unreadable required files.

For state-specific recovery steps, see [Troubleshooting](troubleshooting.md).
