# Task Authoring

Use this reference to create or repair a Galley task YAML file.

## Completion Boundary

A task is ready when the executor can implement one observable outcome without choosing a new product requirement or an unapproved durable design decision.

Determine the stopping point from the user's request:

- acceptance criteria only: present the criteria and stop;
- task authoring: create and validate a draft, then stop;
- queueing: create and queue the task;
- queueing with daemon startup: queue the task, start the requested background daemon, and return after startup succeeds.

## Authoring Flow

### 1. Gather Decision-Relevant Evidence

Resolve the target repository from the current git root or an explicit user path. Inspect:

- supplied issues, specifications, plans, reviews, logs, or screenshots;
- repository instructions and the responsibility that owns the changed behavior;
- representative callers, consumers, and tests when they can change scope or verification;
- resolved Galley profiles and repository-owned quality commands.

Treat a supplied link or document as an evidence source. Add it to `files[]` only when the executor needs a local copy that cannot be replaced by the extracted task contract. Context-only is the default for copied evidence; commit it only when the user requested it as repository output.

Stop investigating when more evidence cannot change the outcome, preserved boundary, expected edit scope, or verification method.

### 2. Complete the Task Contract

Apply `references/authoring-quality.md` to determine:

- one observable goal;
- binding acceptance criteria;
- behavior and contracts that this change must preserve;
- the responsibility and paths expected to change;
- the narrowest verification that can falsify each criterion;
- any product or durable design decision the executor must not invent.

Ask one focused question only when missing information prevents this contract from being written. Resolve repository-local reversible choices from evidence and leave implementation details to the executor.

### 3. Resolve Profiles and Explicit Overrides

Run:

```bash
galley profile resolve --cwd <absolute-target-repo> --output json
```

Use existing profile and skeleton defaults without presenting them as task decisions. Switch to profile authoring only when a required profile is missing or the user requested a profile change.

Resolve the effective executor before queueing. When task `executor.cli` is set, use its task model and effort as authored and let empty values fall through to that CLI; otherwise fill empty task fields from the environment profile, default an empty CLI to Claude, and leave any remaining model or effort to the selected CLI. For a resolved GLM or Kimi executor, verify the matching daemon API key; for Grok, verify the installed CLI is authenticated.

Write `executor.cli`, `executor.model`, or `executor.effort` only when the user explicitly selected the override. Preserve the exact model value supplied by the user. When compatibility is unknown and can make execution fail, verify it with the selected CLI or report the exact unresolved compatibility issue.

### 4. Generate and Fill the Skeleton

Create new tasks with the bundled script:

```bash
python3 <this-skill-directory>/scripts/create_task_skeleton.py "<short task title>" \
  --cwd <absolute-target-repo> \
  --output-dir <draft-dir> \
  --allowed-path <relative-path>
```

Add only explicitly selected non-default flags. Edit the generated fields rather than recreating the schema by hand.

Fill the task as follows:

- `goal`: the single observable outcome;
- `acceptance_criteria`: the smallest set that proves the outcome and material preserved boundaries;
- `scope.allowed_paths`: the expected implementation responsibility and any verification file required for the same outcome;
- `scope.forbidden_paths`: paths that need mechanical protection;
- `decisions`: binding product, contract, or durable design choices the executor must preserve; otherwise `[]`;
- `risks`: evidence-backed material risks that change implementation or verification; otherwise `[]`;
- `files`: local evidence the executor must read, with a safe workspace destination and commit policy;
- `executor`: explicit task overrides only.

Use the generated execution policy and worktree values unless the user selected different values.

### 5. Validate, Queue, and Report

When the requested stopping point is a validated draft, run:

```bash
galley task validate <task-file>
```

If validation reports a shape error, preserve the task contract, repair the generated skeleton against `references/task.schema.json`, and validate again.

When queueing is authorized, run:

```bash
galley task queue <task-file>
```

`task queue` validates before publishing the task. Repair and retry only when it rejects the draft.

When queueing is not yet authorized, validate first and present only:

- goal and acceptance criteria;
- expected edit scope and preserved boundaries;
- explicit executor or policy overrides;
- copied input files and commit policy, when any;
- external effects that differ from the existing approved profile.

Ask once whether to queue that validated task. An affirmative reply to this concrete summary authorizes queueing and any daemon action included in the same summary.

After queueing, use `references/handoff-and-queueing.md` for the requested daemon action. Do not continue into implementation or monitoring unless the user requested it.

## Field Notes

- `scope.permission`: use `sandbox-full-access` for normal isolated AFK implementation, `read-only` for investigation, and `edit` when broad worktree operations are unnecessary.
- `scope.allowed_paths` is the expected write set reviewed by the supervisor, not a reason to omit an adjacent file required by the same accepted outcome.
- `execution_policy.loop_budget`: use the skeleton default unless the user selected a different retry budget. `0` is unlimited.
- `execution_policy.timeout_ms`: use the skeleton default unless repository evidence or the user requires another per-attempt limit.
- `preflight.acceptance_skeleton.enabled`: enable only when the user selected it or an existing accepted verification contract requires a pre-created integration/E2E boundary. Otherwise retain the skeleton default.
- `worktree.branch` and `worktree.path`: keep the generated isolated sibling-worktree values.
