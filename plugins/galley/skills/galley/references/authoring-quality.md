# Authoring Quality

Use this reference when creating task YAML, quality profiles, or environment profiles. It defines how to turn user intent, repository evidence, and supplied planning files into executable Galley inputs.

## Source Priority

Build from concrete sources before asking questions. Use sources that the user supplies or that already exist in the repository; treat planning docs as inputs, not prerequisites.

1. User-provided files or paths: specs, work plans, PRDs, issue exports, design notes, review comments, logs, screenshots, API docs, schema docs, or investigation notes.
2. Repository-local instructions: `SKILL.md`, `AGENTS.md`, `CLAUDE.md`, `README.md`, `CONTRIBUTING.md`, `docs/`, CI config, package scripts, Makefiles, justfiles, and existing tests.
3. Existing Galley files: `~/.galley/profiles/<repo-key>/*.yaml`, previous task YAML, run evidence, and supervisor verdicts.
4. Targeted user answers for remaining gaps.

When an available file contains the goal, ACs, constraints, or test plan, extract from the file and ask the user to confirm only ambiguous or risky points.

## Task Essence

Before writing YAML, identify the task's fundamental purpose. This keeps the task from becoming a shallow command list.

| User wording | Essence to capture |
| --- | --- |
| Fix this bug | root cause, affected behavior, regression proof |
| Add this feature | user-visible value, contract changes, verification path |
| Refactor this | target quality problem, preserved behavior, rollback boundary |
| Update docs | audience, changed behavior, examples that must stay true |
| Investigate this | evidence to collect, decision to enable, stopping point |

Use the essence to shape `goal`, ACs, risks, and verification. Record the surface request directly when it satisfies all Goal checks below; otherwise convert it into a concrete goal before drafting.

## Reference Files

Use `files[]` for implementation reference material the executor should receive in the worktree, such as specs, plans, issue exports, review notes, logs, screenshots, API contracts, schema excerpts, fixtures, migration notes, or design handoff text.

For each reference file, set:

| Question | Guidance |
| --- | --- |
| Why is this file needed? | Record the reason in `description`. |
| Where should it be copied? | Choose an execution-workspace destination the executor can read. |
| Should it be committed? | Treat specs, logs, screenshots, and temporary plans as context-only by default. Include a supplied file in the final branch only when the user intends it to become repository output. |

Task authoring owns intake timing, missing-detail prompts, and unreadable-path handling. Use completed reference content to extract goal, ACs, boundaries, risks, and verification signals before drafting.

## Goal Quality

A Galley task goal should be one observable outcome.

Good goal:

```text
Add result filtering so callers can restrict returned records by approved attributes.
```

Weak goal:

```text
Improve search.
```

Goal checks:

- Names the affected product area or workflow.
- Describes the user-visible behavior or operational outcome.
- Focuses on one coherent work item.
- Can be judged complete without reading the executor's private reasoning.

## Acceptance Criteria Quality

Each AC must be explicit, testable, and useful to the supervisor.

Draft ACs value-first:

1. State the user, operator, or maintainer value the task must deliver.
2. Define one or more observable ACs that prove that value is delivered.
3. Add technical boundaries, regressions, and checks when the user/spec requires them or repository investigation shows they are needed to preserve the delivered value or existing accepted behavior.

Use ACs for:

- requested behavior
- compatibility or regression constraints
- API/schema/CLI contract consistency
- required quality checks
- documentation or operator guidance when usage changes

Write ACs as observable behavior. Include implementation-shape ACs only when the implementation itself is the requirement.

Classify each item by obligation before keeping it as an AC. An AC states a required observable outcome; route how-the-code-is-built choices and out-of-scope items to `decisions` or `risks` instead. When an item is a required outcome but its verification is weak, keep it as an AC and strengthen the verification — replace text like "tests pass" with the specific command, review evidence, or profile check that proves it — rather than demoting it. State each AC as the outcome required, not as a prohibition on an internal mechanism.

Prefer EARS-style wording when it fits the behavior. EARS means "Easy Approach to Requirements Syntax" and makes trigger, condition, and expected behavior explicit.

| Pattern | Use For | Shape |
| --- | --- | --- |
| Event | user action, API call, command, job, hook | `When <trigger>, the system shall <observable behavior>.` |
| State | behavior during a state | `While <state>, the system shall <observable behavior>.` |
| Conditional | branch or error handling | `If <condition>, then the system shall <observable behavior>.` |
| Ubiquitous | always-true behavior | `The system shall <observable behavior>.` |

Task YAML AC format:

```yaml
- id: AC1
  text: <observable behavior or constraint>
  verification: <verification method or evidence source; for behavior changes, include the proof obligation; see Proof-Oriented Verification>
  status: pending
```

Good AC:

```yaml
- id: AC1
  text: When a request includes one or more approved filters, the system shall apply those filters before limiting results and return only matching records.
  verification: "<repo test command>; claim: filters apply before limiting; primary failure mode: unfiltered or post-limit results are returned; boundary: observable result set; state: records include mixed attributes -> filtered request runs -> only matching records remain; residual: none."
  status: pending
```

Additional EARS examples:

```yaml
- id: AC2
  text: While stored records contain mixed attributes, the system shall preserve existing ordering behavior when no filter is provided.
  verification: "<repo test command>; claim: unfiltered requests preserve ordering; primary failure mode: default ordering changes without a filter; boundary: observable result ordering; state: mixed records exist -> unfiltered request runs -> existing ordering remains; residual: none."
  status: pending
- id: AC3
  text: If a caller sends a typed filter value, then the system shall validate and compare it according to the documented contract.
  verification: "<repo test command>; claim: typed filter values follow the documented contract; primary failure mode: typed values are compared with the wrong semantics; boundary: request validation and comparison; state: typed filter input -> validation and comparison run -> documented match result; residual: none."
  status: pending
- id: AC4
  text: The user-facing documentation shall describe the filter option with one valid example.
  verification: <repo documentation check>
  status: pending
```

Weak AC:

```yaml
- id: AC1
  text: Implement metadata filtering.
  verification: tests pass
  status: pending
```

AC traceability checks:

- Every must-have requirement from an available spec, issue, or work plan is covered by at least one AC, or listed as out of scope with a reason.
- Every AC has a verification method or evidence source. Prefer profile required checks for runnable commands that must be enforced by Galley.
- Every risky boundary crossing has an AC or quality finding target: API/schema, data persistence, authorization, UI state, CLI output, migration, external service, or config.
- Each AC names one observable obligation. Split one AC when "and" joins independently verifiable obligations or the AC crosses multiple boundaries.
- Each requirement is expanded by invariant before finalizing ACs; see Invariant Expansion below.

### Invariant Expansion

Before finalizing ACs, expand each requirement by invariant: the changed behavior that must remain true across relevant inputs, states, and lifecycle transitions.

For each changed CLI output, JSON payload, config precedence rule, schema field, task state, queue file, process control path, or run evidence, check whether the same invariant affects:

- sibling fields on the same surface
- later lifecycle states or retries
- stale, missing, or empty values
- failed refreshes, failed lookups, or unavailable fallbacks
- publication or visibility boundaries, such as when a file, status, or record becomes observable to another command, daemon loop, or user
- concurrent or reordered execution paths, where new interleavings, races, double-processing, or lost updates become possible

Cover acceptance-relevant cases and behavior-determining paths in ACs. If a related case or path is intentionally out of scope, record the reason in `decisions`, `risks`, or the task summary. Verification should force each acceptance-relevant path when practical, or record why direct coverage is out of scope. Split separate obligations into separate ACs; keep multiple verification paths only for the same obligation.

For bug-fix or behavior-correction tasks, define the smallest reproducing state before finalizing ACs. Include the prior state that makes the bug observable, such as stale local or remote state, cached or generated artifacts, persisted records, existing resources, configuration values, previous run state, or saved history. At least one AC or verification item should prove the fix in that reproducing state.

### Proof-Oriented Verification

For behavior-changing ACs, write the proof obligation into `verification` so the executor and supervisor can judge whether evidence proves the claim, not only whether a command ran.

Encode proof details inside the `verification` string as labeled clauses: `claim: ...; primary failure mode: ...; boundary: ...; state: ...; residual: ...`.

Include these fields when relevant:

| Field | Use |
| --- | --- |
| `claim` | Observable behavior the AC promises. |
| `primary failure mode` | Wrong behavior that should make the check fail. |
| `boundary` | Public or integration boundary the evidence exercises, such as CLI command, API handler, persisted file, UI flow, daemon lifecycle, or in-process unit. |
| `state` | Before -> action -> after assertion for state-changing claims; use `N/A` for stateless claims. |
| `residual` | Acceptance-relevant case left unproven, or `none`. |

Route safe residuals to `risks`. Ask a blocking task question when the residual changes whether the task is safe to queue.

Compact example:

```yaml
- id: AC1
  text: When a context-only reference file is copied into the worktree, the final PR shall exclude that file from the committed diff.
  verification: "Focused queue/final-diff test; claim: context-only inputs inform execution but are not committed; primary failure mode: context-only file appears in final diff; boundary: task file -> worktree copy -> final diff; state: input copied -> executor completes -> diff excludes input; residual: none."
  status: pending
```

## Quality Criteria Sources

If quality standards already exist, use them before inventing general gates.

## Profile-Guided Authoring Steps

When repository profiles exist, use them before inventing ACs, verification, or quality gates:

1. Read the resolved `quality.yaml` and `environment.yaml` for the target repo.
2. Use `quality.required_checks` as the first source for task verification commands.
3. Use `environment.commands` as the first source for runnable command text.
4. Use `quality.review_dimensions[].pass` and `pass_policy` as the baseline for ACs, risks, and quality-basis notes.
5. Use `environment.constraints`, `pr`, and `worktree` settings to shape implementation boundaries, risks, queueing assumptions, and execution-setting explanations.
6. Add repo, CI, or generic checks only for gaps not covered by the profiles.

Runnable verification commands should fail when the checked condition fails. Prefer exact commands from `environment.commands`, `quality.required_checks`, or CI. Treat inspection-only commands as evidence sources unless they are wrapped with a failing assertion.

For observable contracts, choose one expected contract before drafting ACs. CLI text, JSON payloads, files, logs, PR bodies, statuses, titles, and public docs should name the required fields, values, ordering, persistence, or fallback behavior. Record alternatives in `decisions` only after a behavior is chosen.

Carry literal observable values into the AC text, verification, decision, or risk that owns them when any source fixes the value as required: task text, an AC, or an input material names it as required; a public API, CLI, schema, persisted format, or test consumes it; or multiple authoritative existing examples use the same value. Literal values include field and key names, enum/status values, order-sensitive output, fallback or empty-state text, derived display rules, lifecycle negatives such as a value becoming visible only after completion, and config precedence values. Treat these values as part of the contract rather than paraphrasing them into a general description.

Before validation, rewrite draft wording that would make the executor guess:

| Draft wording | Required form |
| --- | --- |
| undecided `optional` behavior | selected choice, omitted behavior, or named condition that enables it |
| `as needed` / `if needed` | trigger condition plus required action |
| `existing behavior` | observable behavior plus source path, test, command output, or public contract |
| `related files` | concrete paths, globs, or search hints |
| `placeholder` | exact temporary value or behavior, replacement owner, and verification expectation |
| required `TBD` | blocking unresolved item with needed input |
| `appropriate` / `proper` | measurable criterion or checklist |

When multiple valid choices remain, record the selected choice in `decisions` or write a deterministic decision rule. Record only execution-safe uncertainty in `risks`.

Look for:

- existing repo quality profile resolved by `galley profile resolve --cwd <repo> --output json`
- repository CI jobs
- package scripts and Makefile/just targets
- test docs in README or CONTRIBUTING
- design system, accessibility, security, or API contract docs
- team skills under `.claude/skills` or `.agents/skills`

When repo-specific standards are absent, choose checks that match the task domain and can run through commands found in the resolved environment profile, CI, package scripts, Makefile/justfile, or repository docs. Explain which generic gates are left out because they add cost without acceptance evidence.

| Domain | Usually useful | Include only when relevant |
| --- | --- | --- |
| Backend/API | unit/integration tests, typecheck, schema/handler/client contract review, data integrity checks | visual checks |
| Frontend/UI | typecheck, unit/component tests, e2e for changed flows, accessibility, screenshots for visual changes | DB migration checks |
| CLI/library | unit tests, help/output compatibility, public API or flag docs | browser screenshots |
| Infra | plan/diff review, blast-radius notes, rollback notes, policy/security checks | app-level e2e |
| Docs-only | link/render checks, example command validation | full application test suite unless docs generate code |

Verification levels help choose the strongest practical evidence:

| Level | Meaning | Use When |
| --- | --- | --- |
| L1 | Functional operation proves the user-visible behavior | CLI command, API call, UI flow, or job can be run locally |
| L2 | Tests prove the changed behavior | unit/integration/component tests are the realistic proof |
| L3 | Build/type/static checks prove consistency | behavior is docs/config/types only, or runtime proof is unavailable |

Prefer L1 when the operation can run locally with available commands and services. Use L2 when L1 needs unavailable services, unsafe operations, or setup outside the environment profile. Use L3 when the change is docs/config/types-only or runtime proof is unavailable.

## Investigation Targets

For non-trivial implementation tasks, include concrete investigation targets in `decisions`, `risks`, or reference file summaries so the executor knows where to look first.

Good targets:

- `<request parsing path>` - request parsing and validation
- `<domain storage/query path>` - ordering, filtering, and limit behavior
- `<relevant regression test path>` - current regression patterns
- `<build/test manifest>` - available test, build, or static-check commands

List specific paths or files as investigation targets when they can be inferred, such as the path that owns request parsing.

## Question Strategy

Ask questions only after inspecting available files and repository evidence.

Ask when:

- goal or success condition is contradictory or missing
- multiple architectures are plausible and the choice is hard to reverse
- required secrets, external services, or destructive operations are involved
- commit policy for a supplied reference file is unclear
- quality gates require a user policy choice beyond repo evidence

Prefer making and recording a reversible decision when the risk is low. Record assumptions in `decisions` and unresolved concerns in `risks`.

A decision is reversible when undoing it requires only editing the task YAML, rerunning the same command, reverting one commit, or applying a small follow-up patch.

Ask for documents only when the missing information blocks safe goal, AC, implementation boundary, or verification definition. Otherwise proceed with the current request and repo evidence.

## Extracting From Existing Planning Docs

When PRDs, design docs, work plans, or task docs exist, use their useful parts without importing their process overhead.

| Source | Extract | Leave Out |
| --- | --- | --- |
| PRD / issue | user value, must-have requirements, ACs, out-of-scope items | broad roadmap items unrelated to the task |
| Design doc | affected paths, contracts, data flow, invariants, verification strategy | long rationale not needed by the executor |
| Work plan | overall objective, ordered implementation steps, dependencies, quality mechanisms, risks | schedule estimates, human assignment details, and phase labels used as task-splitting instructions |
| Existing task doc | investigation targets, target files, operation verification, constraints | task runner instructions that conflict with Galley |

Convert extracted material into Galley fields:

- Goal: one outcome from the source document's objective or task purpose.
- ACs: EARS-style or measurable requirements with verification evidence.
- Execution boundary: target files, protected boundaries, and allowed/forbidden paths.
- Reference files: source documents copied through `files[]` only when the executor benefits from reading them in the worktree.
- Quality basis: existing profile, CI command, documented quality mechanism, or a domain-specific fallback.

## Output Check

Before validation, the authored file should answer:

- What is the goal?
- Which source files or references informed it?
- Which reference files will be copied into the worktree, and will they be committed?
- What exact ACs define done?
- Which checks or evidence will prove each AC, including proof obligations for behavior-changing ACs?
- What paths may the executor change?
- What existing quality/profile guidance was used?
- Which choices, unresolved items, and execution-safe residuals remain?
