# Authoring Quality

Use this reference when creating task YAML, quality profiles, or environment profiles. It defines how to turn user intent, repository evidence, and supplied planning files into executable Galley inputs.

## Source Priority

Build from concrete sources before asking questions. Use sources that the user supplies or that already exist in the repository; treat planning docs as inputs, not prerequisites.

1. User-provided files or paths: specs, work plans, PRDs, issue exports, design notes, review comments, logs, screenshots, API docs, schema docs, or investigation notes.
2. Repository-local instructions: `SKILL.md`, `AGENTS.md`, `CLAUDE.md`, `README.md`, `CONTRIBUTING.md`, `docs/`, CI config, package scripts, Makefiles, justfiles, and existing tests.
3. Existing Galley files: `~/.galley/profiles/<repo-key>/*.yaml`, manifests, previous task YAML, run evidence, and supervisor verdicts.
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

Use the essence to shape `goal`, ACs, risks, and verification. Record the surface request only when it is already precise enough to execute.

## Reference Files

Use `files[]` for implementation reference material the executor should receive in the worktree.

Examples:

- product spec, work plan, implementation plan, task brief, PRD
- API contract, schema excerpt, fixture, migration note
- review feedback, bug report, stack trace, error log
- screenshot, accessibility report, visual QA note
- design handoff text, Figma export, copy deck

For each reference file, decide:

| Question | Guidance |
| --- | --- |
| Why is this file needed? | Record the reason in `description`. |
| Where should it be copied? | Use a relative `destination` under `scope.allowed_paths`, such as `docs/galley-inputs/<name>`. |
| Should it be committed? | Use `commit: false` for context-only specs, logs, screenshots, and temporary plans. Use `commit: true` only when the file is intended to become part of the repository output. |

If the user gives a file path, read the file before drafting when it is accessible, readable as useful text, and inside the permitted read scope. When it cannot be read, record the risk and ask for the missing content or a usable path.

## Goal Quality

A Galley task goal should be one observable outcome.

Good goal:

```text
Add metadata filtering to the MCP search tool so CLI and MCP callers can restrict results by file metadata.
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

Use ACs for:

- requested behavior
- compatibility or regression constraints
- API/schema/CLI contract consistency
- required quality checks
- documentation or operator guidance when usage changes

Write ACs as observable behavior. Include implementation-shape ACs only when the implementation itself is the requirement.

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
  verification: <verification method, evidence source, or runnable command when one is known>
  status: pending
```

Good AC:

```yaml
- id: AC1
  text: When CLI search receives one or more metadata filters, the system shall apply those filters before maxFiles truncation and return only matching files.
  verification: pnpm test -- metadata-filter
  status: pending
```

Additional EARS examples:

```yaml
- id: AC2
  text: While the search index contains files with mixed metadata, the system shall preserve existing unfiltered ranking behavior when no metadata filter is provided.
  verification: pnpm test -- metadata-filter
  status: pending
- id: AC3
  text: If an MCP caller sends a numeric or boolean metadata filter value, then the system shall validate and compare it according to the documented metadata contract.
  verification: pnpm test -- metadata-filter
  status: pending
- id: AC4
  text: The CLI help shall document the metadata filter flag with one valid example.
  verification: pnpm start -- --help
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

## Quality Criteria Sources

If quality standards already exist, use them before inventing general gates.

Look for:

- existing repo quality profile resolved by `galley profile resolve --cwd <repo> --output json`
- repository CI jobs
- package scripts and Makefile/just targets
- test docs in README or CONTRIBUTING
- design system, accessibility, security, or API contract docs
- team skills under `.claude/skills` or `.agents/skills`

When repo-specific standards are absent, choose only checks that match the task domain and can realistically run locally. Explain which generic gates are intentionally left out because they add cost without useful evidence.

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

Prefer L1 over L2 over L3 when the cost is reasonable. Use lower levels with a stated reason when higher-level proof is unavailable or not useful.

## Investigation Targets

For non-trivial implementation tasks, include concrete investigation targets in `decisions`, `risks`, or reference file summaries so the executor knows where to look first.

Good targets:

- `src/server/index.ts` - request parsing and validation
- `src/vectordb/index.ts` - search ordering, filtering, and maxFiles behavior
- `tests/search.test.ts` - current regression patterns
- `package.json` - available test/typecheck commands

List specific paths or files as investigation targets when they can be inferred, such as `src/server/index.ts` for request parsing.

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

Ask for documents only when the missing information blocks safe goal, AC, scope, or verification definition. Otherwise proceed with the current request and repo evidence.

## Extracting From Existing Planning Docs

When PRDs, design docs, work plans, or task docs exist, use their useful parts without importing their process overhead.

| Source | Extract | Leave Out |
| --- | --- | --- |
| PRD / issue | user value, must-have requirements, ACs, out-of-scope items | broad roadmap items unrelated to the task |
| Design doc | affected paths, contracts, data flow, invariants, verification strategy | long rationale not needed by the executor |
| Work plan | phase/task objective, dependencies, quality mechanisms, risks | schedule estimates and human assignment details |
| Existing task doc | investigation targets, target files, operation verification, constraints | task runner instructions that conflict with Galley |

Convert extracted material into Galley fields:

- Goal: one outcome from the source document's objective or task purpose.
- ACs: EARS-style or measurable requirements with verification evidence.
- Scope: target files, protected boundaries, and allowed/forbidden paths.
- Reference files: source documents copied through `files[]` only when the executor benefits from reading them in the worktree.
- Quality basis: existing profile, CI command, documented quality mechanism, or a domain-specific fallback.

## Output Check

Before validation, the authored file should answer:

- What is the goal?
- Which source files or references informed it?
- Which reference files will be copied into the worktree, and will they be committed?
- What exact ACs define done?
- Which checks or evidence will prove each AC?
- What paths may the executor change?
- What existing quality/profile guidance was used?
- Which assumptions remain?
