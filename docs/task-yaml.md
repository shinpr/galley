# Task YAML

Task YAML is the trusted local input that tells Galley what to execute, where to execute it, how to judge completion, and how to handle PR automation.

Use the plugin skill for normal authoring. This document is the reference for reviewing or hand-writing a task file.

## Starter Template

This template includes the fields needed for validation plus the runtime sections Galley updates later. Start here when writing a task by hand.

```yaml
id: "task-20260509-example"
mode: "afk"
status: "draft"
goal: "Implement the requested repository change with evidence."
acceptance_criteria:
  - id: "AC1"
    text: "When the requested behavior is exercised, the observable result matches the request."
    verification: "A focused check or test demonstrates the behavior."
    status: "pending"
scope:
  cwd: "/path/to/repo"
  allowed_paths:
    - "."
  forbidden_paths:
    - ".env"
    - ".env.local"
  permission: "sandbox-full-access"
execution_policy:
  loop_budget: 10
  timeout_ms: 1200000
  afk_decision_policy: "choose-smallest-reversible"
  stop_on_destructive_operation: true
  stop_on_missing_secret: false
  stop_on_external_service_unavailable: false
worktree:
  enabled: true
  branch: "agent/task-20260509-example"
  path: "../repo.worktrees/task-20260509-example"
supervisor:
  review_iterations: 0
executor:
  cli: "claude"
  model: "opus"
  effort: "high"
  prompt_profile: "codexized-claude-executor-v1"
  prompt_mode: "replace"
  max_budget_usd: 10
decisions: []
risks: []
attempts: []
verification:
  commands: []
pr:
  url: ""
  status: ""
```

Validate before queueing:

```sh
galley task validate ./TASK.yaml
```

Queue after review:

```sh
galley task queue ./TASK.yaml --reason "queue for daemon"
```

## Fields

- `id`: stable task identifier. Use letters, numbers, dot, underscore, and dash.
- `mode`: currently `afk`.
- `status`: use `draft` before queueing. Galley updates this as the task moves through the queue.
- `goal`: concise objective for the work.
- `acceptance_criteria[]`: observable completion requirements with stable IDs such as `AC1`.
- `files[]`: optional user-supplied files to place in the execution workspace.
- `scope`: repository path, allowed/forbidden paths, and permission level.
- `execution_policy`: attempt budget, timeout, and escalation behavior.
- `worktree`: isolated branch and sibling worktree location for AFK execution.
- `supervisor`: review loop settings.
- `executor`: executor CLI and model settings.
- `decisions`, `risks`, `attempts`, `verification`, `pr`: audit and runtime state updated by Galley.

## Execution Policy And Executor

- `execution_policy.loop_budget`: non-negative attempt count; `0` means unlimited.
- `execution_policy.afk_decision_policy`: currently `choose-smallest-reversible`; the executor should choose the smallest reversible option when it can continue without human input.
- `execution_policy.stop_on_destructive_operation`: stop when the task would require out-of-scope destructive work.
- `execution_policy.stop_on_missing_secret`: stop when a required secret is unavailable and cannot be replaced by safe local evidence.
- `execution_policy.stop_on_external_service_unavailable`: stop when a required external service is unavailable and the task cannot proceed with local substitutes.
- `executor.cli`: selects the executor backend. Accepts `claude` (Claude Code) or `codex` (`codex exec`). The daemon dispatches the implementation attempt through the matching binary and persists per-attempt evidence under `runs/<run-id>/attempt-N/` for both. The Codex executor reuses the existing Claude executor prompt content in this iteration; no Codex-tuned executor prompt asset is introduced.
- `executor.model` and `executor.effort`: model selection hints for the executor command. For `codex`, effort is delivered via `-c model_reasoning_effort=<value>` because `codex exec` does not expose a top-level `--effort` flag.
- `executor.prompt_profile`: prompt profile name recorded for evidence.
- `executor.prompt_mode`: `replace` or `append`.
- `executor.max_budget_usd`: non-negative execution budget hint. Claude honors this hint via its CLI flag; `codex exec` has no equivalent flag, so the value is recorded for audit and the runner surfaces an informational warning. See `examples/afk-task-codex.yaml` for a complete Codex example.

## Permissions

| Permission | Meaning |
| --- | --- |
| `read-only` | Read, inspect, and review only. |
| `edit` | Normal implementation work. File edits are allowed, but broad authority is not granted. |
| `sandbox-full-access` | Broad operations are allowed inside an isolated worktree or sandbox. |

For AFK implementation tasks, prefer `sandbox-full-access` with an isolated worktree. Use `read-only` for investigation or review tasks.

`scope.permission` is an authority intent passed into the executor workflow. Actual isolation comes from the worktree, `scope.forbidden_paths`, the executor CLI sandbox, and local OS or container controls. `scope.allowed_paths` describes the expected implementation area and input-file destinations; supervisor-accepted scope expansion can still be committed when it stays outside `scope.forbidden_paths`.

## Input Files

Use `files[]` for specification documents, work plans, screenshots, data samples, or other context the executor needs inside the worktree.

```yaml
files:
  - source: "/tmp/spec.md"
    destination: "docs/input/spec.md"
    description: "Feature specification"
    commit: false
```

- `source` may be absolute or relative to the task YAML before queueing.
- `destination` is relative to the execution workspace and must be inside `scope.allowed_paths`.
- `commit: false` marks context-only input. Galley removes those files before commit/PR finalization.

## Worktree Path

`worktree.path` is relative to `scope.cwd` and must point to a sibling path outside the source repository. `galley task validate` rejects absolute paths, deep parent traversal, and paths that do not start with `../`.

```yaml
worktree:
  enabled: true
  branch: "agent/task-20260509-example"
  path: "../repo.worktrees/task-20260509-example"
```

This keeps the source repository clean while the executor edits the isolated worktree.

## Acceptance Criteria

Write ACs as externally observable obligations. Good ACs name the behavior, the relevant boundary, and the evidence expected.

Use `verification` for evidence guidance. Required runnable checks belong in the quality profile; task-level verification explains why a check proves the AC.

Example:

```yaml
- id: "AC1"
  text: "When `galley task show TASK_ID` is run for a failed task, the output includes the latest failure reason and supervisor verdict."
  verification: "Create or use a failed task fixture and confirm `galley task show` prints both fields."
  status: "pending"
```

## Acceptance Skeleton Preflight

`preflight.acceptance_skeleton` is an optional, default-disabled stage that runs after input files are prepared and before the first executor attempt. When `enabled: true`, Galley runs the built-in test creator, writes AC-linked test skeletons in the worktree, records `runs/<run-id>/preflight_result.json` as the runtime source of truth, updates the running task with the generated skeleton metadata, and adds a skeleton-obligations section to the executor work order.

```yaml
preflight:
  acceptance_skeleton:
    enabled: true
    required: true            # default true when enabled; require each AC to have output or no_skeletons
    allowed_paths:            # optional; defaults to scope.allowed_paths
      - "internal"
    outputs:                  # daemon-owned; written after the built-in creator runs
      - ac_id: "AC1"
        path: "internal/foo/foo_test.go"
        kind: "go-test"
        purpose: "Verify the AC1 behavior boundary"
        satisfies: "AC1's observable foo behavior"
        integration_point: "Executor completes this skeleton before final acceptance"
        implementation_required: true
```

The built-in creator reads the task, ACs, allowed paths, resolved profiles, and task input files such as design docs or work plans; writes AC-linked test skeleton files; and returns a manifest. Galley validates that manifest, writes `preflight_result.json`, updates the running task file with generated `outputs[]`, and annotates each AC's `verification` with the skeleton path, what it satisfies, and the integration point before the executor starts. Generated paths must be relative, inside the effective allowed paths, outside `scope.forbidden_paths`, and backed by real files already written by the creator.

Required-check acceptance gate semantics: this gate is part of the acceptance skeleton stage and runs only when `preflight.acceptance_skeleton.enabled: true` — tasks that omit or disable the section keep the pre-feature accepted-verdict behavior. For preflight-enabled tasks, an accepted verdict is downgraded to `needs_supervisor_review` when a required quality-profile check has no passing verification evidence. `preferred_commands` is treated as an **ordered fallback list**, mirroring how `result.Complete` runs them: the commands run in order, the first that passes is recorded, and only that command's evidence is kept (or the last failure when every command failed). The gate therefore considers a required check satisfied when *any* of its `preferred_commands` has a passing entry, failed when none passed but at least one has a failed entry, and missing only when there is no evidence for any of its commands (which happens when no executor result was produced, e.g. a hard stop). Requiring every fallback command to have evidence would contradict the fallback semantics and is intentionally not done.

`required: false` relaxes AC coverage only: Galley no longer requires every AC to have an output or a `no_skeletons[]` reason. It does not disable required quality-check gating.

## Loop Budget

`execution_policy.loop_budget` is the maximum number of executor attempts. It accepts an integer greater than or equal to `0`; `0` means unlimited.

For AFK implementation tasks, `10` is the recommended default. Values below `5` are best reserved for intentionally short, low-cost runs because they can stop useful revision loops too early. Use `0` only when the user explicitly wants an unbounded run.

`supervisor.review_iterations` controls supervisor-only follow-up iterations. `0` means Galley uses the supervisor as an acceptance gate after executor attempts, without additional supervisor-only review loops.

## Queue State

Task files move through the daemon root:

```text
tasks/queued/
tasks/running/
tasks/done/
tasks/failed/
tasks/archived/
```

Terminal state remains in the YAML. Use:

```sh
galley task list
galley task show TASK_ID
galley task requeue TASK_ID --reason "retry after transient failure"
```

Requeue is useful for transient failures such as usage limits or temporary service errors.
