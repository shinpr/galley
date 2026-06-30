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
  effort: "high"
  prompt_profile: "codexized-claude-executor-v1"
  prompt_mode: "replace"
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
- `scope`: repository path, expected implementation paths, protected paths, and permission level.
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
- `executor.cli`: selects `claude` (Claude Code) or `codex` (`codex exec`). New task authoring uses `environment.yaml` `executor.default_cli` when present, otherwise Claude. An explicit task YAML value is authoritative for that task.
- `executor.model`: optional model override. Omit it to use the selected CLI's configured default model.
- `executor.effort`: model effort hint. Claude accepts `low`, `medium`, `high`, `xhigh`, or `max`; Codex accepts `low`, `medium`, or `high`.
- `executor.prompt_profile`: prompt profile name recorded for evidence.
- `executor.prompt_mode`: `replace` or `append`.
- `executor.max_budget_usd`: optional non-negative execution budget hint. Claude tasks can use it as a per-run ceiling; Codex has no equivalent enforced flag.

## Permissions

| Permission | Meaning |
| --- | --- |
| `read-only` | Read, inspect, and review only. |
| `edit` | Normal implementation work. File edits are allowed, but broad authority is not granted. |
| `sandbox-full-access` | Broad operations are allowed inside an isolated worktree or sandbox. |

For AFK implementation tasks, prefer `sandbox-full-access` with an isolated worktree. Use `read-only` for investigation or review tasks.

`scope.permission` is an authority intent passed into the executor workflow. Actual isolation comes from the worktree, `scope.forbidden_paths`, the executor CLI sandbox, and local OS controls.

`scope.allowed_paths` is the expected implementation scope and review baseline. Executors should stay inside it when the task can be completed there, but a required acceptance criterion or pending revision request may justify a minimal outside-allowed change. Those changes are reported as scope expansions for supervisor and PR review. Each reported expansion path is a POSIX-style worktree-relative clean file path, or the smallest segment-aware directory prefix that covers multiple required outside-allowed changed files. `scope.forbidden_paths` remains the protected path boundary.

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

`preflight.acceptance_skeleton` is an optional stage that creates AC-linked test skeletons before the first executor attempt. When enabled, Galley records the result and adds skeleton obligations to the executor work order.

New task skeletons include the stage in its default disabled state:

```yaml
preflight:
  acceptance_skeleton:
    enabled: false
```

Disabled preflight has the same daemon behavior as omitting `preflight` entirely. Set `enabled` to `true` when the task should create acceptance-criterion-linked skeleton files before the executor starts.

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

The built-in creator writes AC-linked skeleton files and returns a manifest. Generated paths must be relative, inside the effective allowed paths, outside `scope.forbidden_paths`, and backed by real files.

The skeleton creator follows `executor.cli` and reuses `executor.model` and `executor.effort`. The daemon supervisor backend controls only review.

When this stage is enabled, required quality-profile checks must have passing verification evidence before an accepted verdict can stay accepted. `preferred_commands` are ordered fallbacks: the first passing command satisfies the check.

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

## Lenient Task YAML Handling

Read-only commands tolerate older task files with unknown fields so operators can still inspect or archive them. Active intake stays strict: `validate`, `queue`, `requeue`, and daemon execution reject unknown fields or type mismatches before work reaches an executor.
