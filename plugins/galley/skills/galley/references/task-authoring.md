# Task Authoring

Use this reference when creating or repairing a Galley task YAML file.

## Authoring Flow

1. Identify the target repository.
2. If the user supplied planning/reference files or the repository already has relevant PRD, design, work plan, issue, or review docs, read them. Extract task essence, goal, ACs, scope, exclusions, constraints, verification, and risks from them.
3. Inspect repository guidance and existing Galley profiles.
4. Apply the question strategy in `references/authoring-quality.md` for gaps that remain after available file and repo inspection.
5. Draft task YAML, validate it, then summarize it for approval.

Use `references/authoring-quality.md` for goal, AC, quality gate, reference file, and question strategy rules.

## Inputs To Resolve

Resolve these fields before writing YAML:

| Field | Required Detail |
| --- | --- |
| target repository | absolute path for `scope.cwd` |
| objective | one sentence describing the user-visible outcome |
| task essence | fix, feature, refactor, docs, investigation, or mixed; plus the real problem being solved |
| scope | files or directories the executor may change |
| exclusions | secrets, generated files, git internals, unrelated areas |
| permission | `read-only`, `edit`, or `sandbox-full-access` |
| reference files | optional specs, work plans, logs, screenshots, issues, or docs; where they should be copied in the worktree; and whether they should be committed |
| acceptance criteria | observable requirements with verification evidence |
| quality checks | tests, typecheck, lint, build, e2e, screenshots, or manual evidence |
| loop budget | retry count or `infinite` |
| PR policy | whether accepted work should open a PR |

Use `references/authoring-quality.md` for reversible-decision criteria, escalation criteria, EARS AC writing, traceability, and question strategy.

## Repository Context

Inspect the target repo before drafting:

```bash
cd <target-repo>
pwd
git status --short
find . -maxdepth 3 -name SKILL.md -o -name AGENTS.md -o -name CLAUDE.md -o -name README.md
```

Then read the local guidance files that exist. If project skills exist under `.claude/skills`, `.agents/skills`, or another team skill directory, incorporate only the skills relevant to the task domain.

Also inspect existing quality inputs when present:

```bash
galley profile resolve --cwd <absolute-target-repo> --output json
find .github -maxdepth 3 -type f 2>/dev/null
```

Use the same absolute target repository path for `galley profile resolve --cwd` and task `scope.cwd`. If the current shell is already in the target repository, `$PWD` is acceptable; otherwise pass the explicit target path.

## Acceptance Criteria

Write ACs as testable statements. Use `references/authoring-quality.md` for EARS patterns and traceability checks. Use this YAML shape:

```yaml
- id: AC1
  text: <observable requirement>
  verification: <verification method or evidence source>
  status: pending
```

Common AC groups:

- Functional behavior: command, API, UI, CLI, or data behavior the user requested.
- Regression behavior: old behavior that must continue.
- Contract behavior: schema, type, handler, caller, or public API consistency.
- Quality behavior: required tests, typecheck, lint, build, e2e, accessibility, or screenshot evidence.
- Documentation behavior: user-facing or operator docs updated when the feature changes usage.

## Task YAML Template

Use this as a starting point and replace placeholders before validation:

```yaml
id: task-YYYYMMDD-short-name
mode: afk
status: draft
goal: <one concrete outcome>
acceptance_criteria:
  - id: AC1
    text: <observable requirement>
    verification: <verification method or evidence source>
    status: pending
scope:
  cwd: /absolute/path/to/repo
  allowed_paths:
    - <relative/path/or/directory>
  forbidden_paths:
    - .env
    - .env.local
    - .git
  permission: sandbox-full-access
files:
  - source: /absolute/path/to/context.md
    destination: docs/context.md
    description: Optional context file supplied by the user.
    commit: false
execution_policy:
  loop_budget: 2
  timeout_ms: 1800000
  afk_decision_policy: choose-smallest-reversible
  stop_on_destructive_operation: true
  stop_on_missing_secret: true
  stop_on_external_service_unavailable: true
worktree:
  enabled: true
  branch: agent/<short-name>
  path: ../<repo-name>.worktrees/<short-name>
supervisor:
  review_iterations: 0
executor:
  cli: claude
  model: opus
  effort: high
  prompt_profile: codexized-claude-executor-v1
  prompt_mode: replace
  max_budget_usd: 4
decisions: []
risks: []
attempts: []
verification:
  commands: []
pr:
  url: ""
  status: ""
  processed_comment_ids: []
```

## Field Guidance

- `mode`: use `afk` for daemon execution.
- `status`: write new tasks as `draft`; `galley task queue` writes the queued copy with `status: queued`.
- `scope.permission`: prefer `sandbox-full-access` for AFK implementation tasks that run in an isolated worktree or sandbox; use `read-only` for investigation and review only; use `edit` when edits are needed but broad sandbox authority is unnecessary or unavailable.
- `allowed_paths`: choose the narrowest paths that still allow the task to succeed.
- `files`: use it when the user attaches or names specs, work plans, logs, screenshots, issue exports, or other implementation references the executor should read in the worktree.
- `files[].source`: use an absolute path or a path relative to the task YAML file. Galley resolves relative sources before queueing or requeueing.
- `files[].destination`: use a relative path inside the execution workspace. It must stay within `scope.allowed_paths`, must not use parent traversal, and must not overwrite an existing file.
- `files[].commit`: use `false` for context-only inputs; use `true` only when the supplied file is intentionally part of the final branch.
- `execution_policy.loop_budget`: use `2` for normal work, `3` for ambiguous multi-file work, `infinite` only when the user explicitly requests an unbounded loop.
- `worktree.path`: use a sibling path outside the source repo, such as `../<repo-name>.worktrees/<short-name>`.
- `supervisor.review_iterations`: start at `0`; Galley increments it when reviewed work is requeued.
- `executor.prompt_mode`: use `replace` for Codex-style Claude executor prompts unless the user asks to preserve Claude Code's base prompt.

## Output Before Validation

After drafting, summarize:

```markdown
Task file: <path>
Goal: <goal>
Task essence: <fix|feature|refactor|docs|investigation|mixed> / <why this task exists>
ACs:
- AC1: <text> / <verification>
Scope:
- cwd: <path>
- allowed: <paths>
- forbidden: <paths>
Reference files:
- <destination>: <source> / commit=<true|false> / <why it is needed>
Quality basis:
- <existing profile, CI command, repo doc, or inferred domain gate>
Investigation targets:
- <path>: <why the executor should inspect it first>
Decisions:
- <decision or none>
Risks:
- <risk or none>
```
