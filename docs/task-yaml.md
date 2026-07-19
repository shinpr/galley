# Task YAML

Task YAML is the trusted local input that tells Galley what to execute, where to execute it, and how to judge completion. Repository-wide PR behavior belongs in `environment.yaml`.

Use the plugin skill for normal authoring. This document is the reference for reviewing or hand-writing a task file.

## Starter Template

This template contains author-owned fields. Galley adds fixed AFK defaults and lifecycle state during validation and execution.

```yaml
id: "task-20260509-example"
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
  timeout_ms: 3600000
worktree:
  branch: "agent/task-20260509-example"
  path: "../repo.worktrees/task-20260509-example"
executor:
  cli: "claude"
  effort: "high"
preflight:
  acceptance_skeleton:
    enabled: false
decisions: []
risks: []
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
- `mode`: optional; defaults to `afk`.
- `status`: optional for authors; defaults to `draft` before validation, display, and queue eligibility. Queueing persists `queued` through the normal transition.
- `goal`: concise objective for the work.
- `acceptance_criteria[]`: observable completion requirements with stable IDs such as `AC1`.
- `files[]`: optional user-supplied files to place in the execution workspace.
- `scope`: repository path, expected implementation paths, protected paths, and permission level.
- `execution_policy`: author-chosen attempt budget and timeout only (`loop_budget`, `timeout_ms`).
- `worktree`: isolated branch and sibling worktree location. `enabled` defaults to true for AFK; authors set `branch` and `path`.
- `executor`: optional per-field overrides for CLI, model, and effort.
- `preflight.acceptance_skeleton.enabled`: optional boolean that selects the fixed skeleton preflight flow.
- `decisions`, `risks`: author and executor notes.
- `supervisor`, `attempts`, `verification`, `pr`: daemon-owned runtime state populated during lifecycle transitions. An attempt that ended in an executor interruption records `supervisor_verdict: not_reviewed` and an executor-phase `error` with the retained provider detail; see [Troubleshooting](troubleshooting.md#executor-interruption).

## Execution Policy And Executor

- `execution_policy.loop_budget`: non-negative attempt count; `0` means unlimited. Omitted values default to `10`.
- `execution_policy.timeout_ms`: positive per-attempt timeout in milliseconds.
- Galley fixes the AFK decision policy at `choose-smallest-reversible`.
- Destructive-operation, missing-secret, and external-service blockers remain executor results; task YAML has no blocker switches.
- `executor`: optional. Each `cli`, `model`, and `effort` field resolves from the task, then current `environment.yaml`. Only `cli` has a built-in fallback (`claude`); an omitted `effort` or `model` lets the selected provider CLI choose its own reasoning effort and model. Environment values remain runtime-only.
- `executor.cli`: selects `claude`, `codex`, `glm`, `grok`, or `kimi`. GLM and Kimi use the `claude` CLI; Grok uses the logged-in `grok` CLI.
- `executor.model`: optional model override. Omit it to use the selected CLI's configured default model.
- `executor.effort`: optional effort hint. Claude, `glm`, and `kimi` accept `low`, `medium`, `high`, `xhigh`, and `max`; Codex also accepts `minimal`; Grok also accepts `none` and `minimal`. Omit it to let the selected provider CLI use its own configured reasoning-effort default. Invalid provider combinations fail before executor roles run.
- Prompt transport is Galley-owned. Task YAML no longer accepts `executor.prompt_profile` or `executor.prompt_mode`; older files that still include those keys are ignored by the unknown-field-tolerant decoder.

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
  branch: "agent/task-20260509-example"
  path: "../repo.worktrees/task-20260509-example"
```

Galley enables this isolated worktree for every AFK task, keeping the source repository clean.

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

`preflight.acceptance_skeleton.enabled` controls AC-linked test skeleton creation before the first executor attempt.

```yaml
preflight:
  acceptance_skeleton:
    enabled: false
```

Enable it when an integration, cross-layer, or E2E acceptance path needs a concrete file before implementation. Leave it disabled when focused tests or required checks already define the evidence path.

When enabled:

- write paths are always `scope.allowed_paths` (and remain outside `scope.forbidden_paths`)
- every AC must produce a skeleton output or an explicit `no_skeletons` reason
- the daemon writes runtime `outputs[]` back to the running task

When a task reuses its existing worktree after a requeue, Galley reuses previously completed setup and acceptance-skeleton evidence instead of running those phases again. A new worktree, or a phase with no prior successful result, still runs normal preflight.

The skeleton creator follows `executor.cli` and reuses `executor.model` and `executor.effort`. The daemon supervisor backend controls only review.

Required quality checks remain executor evidence for supervisor review; the skeleton stage does not add a second command-matching gate.

## Loop Budget

`execution_policy.loop_budget` is the maximum number of executor attempts. It accepts an integer greater than or equal to `0`; `0` means unlimited.

For AFK implementation tasks, `10` is the recommended default. Values below `5` are best reserved for intentionally short, low-cost runs because they can stop useful revision loops too early. Use `0` only when the user explicitly wants an unbounded run.

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

See [Troubleshooting](troubleshooting.md) for the meaning of each terminal status and when to requeue instead of creating a new task.

## Compatibility

The tolerant decoder loads removed authoring keys (`execution_policy.afk_decision_policy`, `execution_policy.stop_on_*`, and acceptance-skeleton `mode`, `required`, and `allowed_paths`), then drops them on save. Runtime lifecycle fields remain available to the daemon.
