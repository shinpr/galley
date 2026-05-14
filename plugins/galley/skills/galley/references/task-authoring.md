# Task Authoring

Use this reference when creating or repairing a Galley task YAML file.

Task authoring has three approvals: task direction, execution settings, then queueing after validation. Keep task questions separate from execution-environment questions. When presenting settings or approval decisions, optimize for referenceability so the user can easily name the item to change.

Use `references/authoring-quality.md` for goal, AC, quality gate, reference file, and question strategy rules. Use `references/task.schema.json` as the machine-readable task YAML contract bundled with this skill.

## Step 1: Ask For Reference Material, Destination, And Commit Policy First

For a new implementation task, first ask one combined question about reference material and file handling. Reference files may contain the goal, requirements, target area, design decisions, boundaries, and verification plan, so complete reference-file intake before collecting additional task details. Ask this as a standalone question before repository investigation, profile resolution, design choices, execution settings, or payload-shape questions. Existing specs or work plans may already contain the goal, ACs, execution boundary, test plan, and design decisions.

If the user already supplied planning/reference files or named an existing PRD, design, work plan, issue, review, log, screenshot, or docs path, treat that as reference-file intake. When path/content, execution-workspace destination, and commit policy are all present, read those files before asking task-design questions. When destination or commit policy is missing, ask for the missing file-handling details first.

Use this standalone prompt:

```markdown
Do you have any specs, work plans, issue links/exports, review notes, logs, screenshots, or other reference files I should use for this task?

If yes, provide these three items for each file:
1. Path or content
2. Destination inside the execution workspace, such as `tmp/work-plan.md` or `doc/work-plan.md`
3. Whether the file should be included in the final commit: `include` or `context-only`

If none, answer `none`.
```

After the user answers:

- A complete reference-file answer contains path/content, destination inside the execution workspace, and commit policy.
- If references are provided with all three items, read them before asking task-design questions.
- If a reference answer only provides a path or content, ask for the missing destination and commit policy before continuing.
- If the user says none, proceed with the request text and repository evidence.
- If a provided path cannot be read, ask for usable content or a different path before relying on that source.
- Carry each completed reference file into task direction and YAML as `files[].source`, `files[].destination`, and `files[].commit`.

If the user's answer includes references but is missing destination or commit policy, ask this compact follow-up:

```markdown
I have the reference file path/content. I still need these two items before continuing:

- Destination inside the execution workspace, for example `tmp/work-plan.md` or `doc/work-plan.md`
- Commit policy: include in the final commit, or context-only
```

## Step 2: Understand Request And Inputs

Identify the target repository and user intent. Extract from the request and available reference material:

- task essence: fix, feature, refactor, docs, investigation, or mixed
- goal
- AC candidates
- implementation boundaries and exclusions
- constraints and risks
- verification signals
- reference files that should be copied into the execution worktree

Use available files when they exist. Treat supplied work plans and design docs as implementation guidance for the single Galley task unless the user explicitly asks for decomposition. Galley executes the approved request as one task. Use the whole supplied work plan or design doc as implementation guidance for that single task. Use task decomposition only when the user explicitly requests it. When available request text or supplied references identify the target product area, carry that target into task direction and ask for approval there. Ask for missing documents only when the missing information blocks goal, AC, implementation boundary, or verification definition.

## Step 3: Inspect Repository And Profiles

Use the current git repository root as the target repository. Galley task authoring is normally run from inside the repository being automated.

Use a user-provided repository path when the user explicitly gives one. When the current shell is outside a git repository and the user has not provided a path, ask for the target repository path before continuing.

Keep the repository choice anchored to the current git root or explicit user path. Treat feature names, tool names, package names, and references to Galley itself as task context inside that repository. Always show the resolved repository path in Step 4 task direction as `Execution target repository` so the user can correct it before approval.

Explore the resolved target repository with the goal of finding enough local guidance to draft a correct task:

- repository root and clean/dirty status
- local agent instructions, project skills, or contribution guidance that apply to the requested task
- README/docs that explain the affected feature or workflow
- CI, package scripts, Makefile/justfile targets, or existing tests that should inform verification
- relevant source areas and tests for the requested change

Stop repository exploration when you can name the repo root, relevant local guidance, affected paths, verification signals, and any dirty-worktree risk. Keep exploration inside the resolved target repository. User-supplied external reference files are handled through Step 1 destination and commit-policy approval.

Resolve existing Galley profiles:

```bash
galley profile resolve --cwd <absolute-target-repo> --output json
```

Use the same absolute target repository path for `galley profile resolve --cwd` and task `scope.cwd`. If the resolved `quality.yaml` or `environment.yaml` does not exist, create profiles with `references/profile-authoring.md` before task drafting continues.

## Step 4: Confirm Task Direction

Before discussing Galley runtime settings or writing task YAML, present the proposed task direction and ask for approval. Include:

- goal
- task essence
- AC direction
- key design decisions to encode
- execution target repository, implementation boundaries, allowed paths, and protected paths
- reference files and commit policy
- reference file execution-workspace destinations
- quality/profile basis

Use a direct approval question, not an open-ended label:

```markdown
Task direction:
- Goal: <goal>
- Acceptance criteria direction: <AC summary>
- Execution target repository: <absolute repo path>
- Implementation boundaries: <included behavior/areas>
- Allowed paths: <paths needed for edits and reference-file destinations>
- Protected paths: <paths>
- Reference files: <files or none>
- Reference file destination: <source -> destination>
- Reference file commit policy: <context-only or committed files>
- Quality basis: <profile or repo evidence>

Approve this task direction before I choose execution settings and create the task YAML?
```

Ask targeted task questions here when they affect correctness, safety, implementation boundaries, allowed paths, or verification. Write YAML only after the user approves the task direction or provides adjustments.

## Step 5: Confirm Execution Settings

After task direction approval, check the current daemon state before proposing execution settings:

```bash
galley daemon status --output json
```

Interpret daemon-dependent settings before asking:

- If a verified daemon is already running, use its current daemon settings as the execution condition. Present supervisor, concurrency, polling interval, claim TTL, heartbeat interval, and shutdown timeout as current daemon state, not user-selectable task options.
- Repository operation settings come from `environment.yaml`: PR creation, PR base branch, PR comment polling/replies, and worktree cleanup.
- If no daemon is running, ask the user to approve the planned daemon startup settings because they will be applied when starting the daemon.
- If daemon status is unclear, report that uncertainty and ask whether to inspect or start a fresh daemon before queueing.

Then propose execution settings with user-facing explanations and choices. Treat `loop_budget: 10` and `timeout_ms: 1800000` as the ordinary-task baseline. Compare the actual task size and verification cost against that baseline using the user request, task direction, repository evidence, and supplied references. Increase timeout and retry budget for broader, slower, or more uncertain tasks; use smaller values when the user explicitly wants a short or low-cost run. Briefly state the comparison evidence.

Execution-setting content requirements:

- Task YAML settings: executor backend, edit authority, retry budget, per-attempt timeout, AC test skeleton preflight, and blocking severity policy.
- Environment profile operation settings: PR behavior, PR base branch, PR comments, and worktree cleanup from the current `environment.yaml`; create missing profiles through `references/profile-authoring.md` before queueing ordinary implementation work.
- Executor backend (`executor.cli`): include it as one Task YAML execution setting. Recommend `claude` by default; offer `codex` when the user wants Codex to perform the implementation work. Explain that this controls the implementation executor for this task and is separate from the daemon supervisor.
- Supervisor: present the planned or current supervisor. Use `claude` as the default review gate and offer `codex` when the user wants Codex review.
- Daemon concurrency: present planned or current `max_concurrent_tasks` and `max_concurrent_per_repo`. Explain that default or low concurrency fits a single heavy task, while higher values are available for parallel execution.
- AC test skeleton preflight (`preflight.acceptance_skeleton.enabled`): include it as one Task YAML execution setting with only `enabled` or `disabled`; recommend `enabled` when AC complexity, regression risk, or task size makes pre-created test skeletons worth the preflight cost, and recommend `disabled` for small, local, low-risk tasks where existing verification commands are enough. Let the skeleton creator derive test kind, path, and content.
- For each user-changeable setting, include the recommended or current value, why it fits the current task, and practical alternatives.
- For settings stored outside task YAML, name the change path, such as `environment.yaml` edits or daemon restart with different flags.

Presentation guidance:

Use a readable format appropriate to the amount of explanation. Prefer concise bullets when reasons are long. Use compact tables only when each entry stays short. The required part is the decision content above, not a specific visual format.

Ask the user to approve the execution settings or name the item to change before generating task YAML.

Record the approved task YAML settings, current quality profile blocking severities, environment profile operation settings, and the current or planned daemon startup settings for the queue/daemon step. The task YAML stores the task content; repository operation behavior is applied from `environment.yaml`; daemon startup behavior is applied by the running daemon or the command used when starting it.

## Step 6: Generate Skeleton

For new tasks, start from the skill-bundled skeleton script instead of writing YAML from scratch. Run the script at `<this-skill-directory>/scripts/create_task_skeleton.py`.

Basic draft:

```bash
python3 <this-skill-directory>/scripts/create_task_skeleton.py "<short task title>" \
  --cwd <absolute-target-repo> \
  --output-dir <draft-dir> \
  --allowed-path <relative-path> \
  --permission sandbox-full-access \
  --loop-budget 10
```

With a context-only specification, work plan, log, screenshot note, issue export, or review note supplied by the user:

```bash
python3 <this-skill-directory>/scripts/create_task_skeleton.py "<short task title>" \
  --cwd <absolute-target-repo> \
  --output-dir <draft-dir> \
  --allowed-path src \
  --allowed-path README.md \
  --allowed-path tmp \
  --reference-file /absolute/path/to/work-plan.md=tmp/work-plan.md
```

Use `--committed-file SOURCE=DESTINATION` only when the supplied file is intentionally part of the final branch.

## Step 7: Fill Skeleton With Schema

Edit the generated skeleton fields instead of replacing the whole file. Use `references/task.schema.json` as the field contract. Keep optional runtime arrays empty until a valid structured entry is needed.

Common shapes:

```yaml
acceptance_criteria:
  - id: AC1
    text: "Observable requirement."
    verification: "Command, test, review evidence, or manual evidence source."
    status: pending

decisions:
  - id: D1
    question: "What decision was made?"
    chosen: "Selected option."
    rationale: "Why this option was chosen."
    reversibility: high
    needs_human_review: false

risks:
  - id: R1
    type: regression
    detail: "What could go wrong."
    mitigation: "How the executor should prevent or detect it."
    human_review_suggested: false

verification:
  commands:
    - cmd: npm test
      status: pending
      output_excerpt: ""
```

Use `decisions: []`, `risks: []`, and `verification.commands: []` when those entries are not necessary.

## Field Guidance

- `mode`: use `afk` for daemon execution.
- `status`: write new tasks as `draft`; `galley task queue` writes the queued copy with `status: queued`.
- `scope.permission`: prefer broad operations inside the isolated worktree (`sandbox-full-access`) for AFK implementation tasks; use investigation only (`read-only`) for review tasks; use normal edits (`edit`) when broad sandbox authority is unnecessary or unavailable.
- `allowed_paths`: choose the narrowest paths that cover approved edits and any reference-file destinations.
- `executor.cli`: use `claude` by default. Use `codex` when the user selects Codex as the implementation executor. This is a task YAML setting and is separate from the daemon supervisor.
- `execution_policy.timeout_ms`: set the approved per-attempt timeout in milliseconds. Use `30min` (`1800000`) as the ordinary-task baseline and increase it for broader, slower, or more uncertain tasks.
- `preflight.acceptance_skeleton.enabled`: set the approved value. When enabled, write the initial preflight config only; runtime `outputs[]` are generated by the skeleton creator.
- `files`: use it when the user attaches or names specs, work plans, logs, screenshots, issue exports, or other implementation references the executor should read in the worktree.
- `files[].source`: use an absolute path or a path relative to the task YAML file. Galley resolves relative sources before queueing or requeueing.
- `files[].destination`: use a relative path inside the execution workspace. It must stay within `scope.allowed_paths`, must not use parent traversal, and must not overwrite an existing file.
- `files[].commit`: use `false` for context-only inputs; use `true` only when the supplied file is intentionally part of the final branch.
- `execution_policy.loop_budget`: use `10` as the ordinary-task baseline for AFK implementation so Galley can complete corrective loops. Increase it for broader or more uncertain tasks. Use less than `5` only when the user explicitly wants a short, low-cost run; use `0` only when the user explicitly requests an unbounded loop.
- Blocking severity is enforced by the resolved quality profile and supervisor policy, not by a top-level task schema field. Show the current profile threshold in execution settings and the final queue summary. Use profile authoring when the user wants to change repository-wide blocking severities. Record task-specific review preferences in `decisions` only as guidance.
- `worktree.path`: use a sibling path outside the source repo, such as `../<repo-name>.worktrees/<short-name>`.
- `supervisor.review_iterations`: start at `0`; Galley increments it when reviewed work is requeued.
- `executor.prompt_mode`: use `replace` for Codex-style Claude executor prompts. Use an append mode when the user asks to preserve Claude Code's base prompt.

## Step 8: Validate And Repair

Validate with the canonical command:

```bash
galley task validate <task-file>
```

Use the positional form, as in `galley task validate path/to/task.yaml`.

If validation reports decode or unmarshal errors:

1. Keep the user-approved goal, ACs, implementation boundaries, allowed/protected paths, and runtime choices.
2. Restore file shape from the skill-bundled skeleton.
3. Repair fields against `references/task.schema.json`.
4. Empty optional arrays that are still failing validation.
5. Run `galley task validate <task-file>` again.

Inspect Galley implementation source only when the target repository is Galley itself.

## Step 9: Summarize For Queue Approval

After validation passes, summarize in two parts: task content, then user-confirmable decisions. This summary is the user's review surface; assume the user will not read the YAML directly.

```markdown
Validation passed for `<task-file>`.

Task content:
| Item | Current content |
| --- | --- |
| Goal | <goal> |
| Acceptance criteria | <AC IDs and short summaries> |
| Execution boundary | repo `<scope.cwd>`; allowed paths `<paths>`; protected paths `<paths>` |
| Reference files | <none, or source -> destination with commit policy> |
| Quality basis | <profile, CI command, repo doc, or inferred domain gate> |
| Primary investigation targets | <paths and why they matter> |

Please confirm these decisions before queueing:
| Item | Current choice | Why it matters | Change options |
| --- | --- | --- | --- |
| Public/API names | <field names, commands, routes, outputs, or N/A> | <compatibility impact> | <rename/change/no change> |
| Behavioral bounds | <timeouts, limits, retries, accepted values, or N/A> | <runtime or product impact> | <change min/max/allowed values> |
| State and persistence | <what is saved or intentionally not saved> | <side-effect impact> | <persist/change/keep isolated> |
| Edit authority | <user-facing authority level> (`<scope.permission>`) | <why this task needs it> | `read-only`, `edit`, `sandbox-full-access` |
| Retry budget | `<execution_policy.loop_budget>` | <why this budget fits the task> | smaller value, larger value, `0` unlimited |
| Per-attempt timeout | <minutes> | <why this fits the repo checks> | shorter or longer timeout |
| Blocking severity policy | <current quality profile blocking severities> | <which finding severities the profile treats as blocking> | change through quality profile authoring before queueing |
| Human-review items | <decisions/risks that need explicit confirmation, or none> | <why user confirmation is useful> | approve, change, move to follow-up |

Daemon status:
| Item | Current / planned |
| --- | --- |
| Current daemon | <running|not running|unknown> |
| Supervisor | <current daemon value if running, planned value if not running> |
| Environment operations | <pr.enabled/pr.base/pr.comments.enabled/pr.comments.reply/worktree.cleanup from environment.yaml> |
| Concurrency | <max_concurrent_tasks/max_concurrent_per_repo current or planned values> |
| Planned command | <exact daemon command if daemon is not running and user approved start, otherwise none> |
| Change path | <daemon startup settings: stop/restart before queueing when running; environment operations: edit environment.yaml before queueing> |
| Effect | <starts automatically after queueing, waits in queue, or uses existing daemon> |

Approve queueing this task with the decisions and daemon plan above?
```

Then use `references/handoff-and-queueing.md` for queue and daemon approval.
