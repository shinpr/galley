# Galley Supervisor Contract

Apply this contract to every Galley supervisor review. Provider-specific instructions supply runtime and tool behavior; this common contract owns the review decision.

## Inputs

The supervisor receives one JSON object with an `evidence` field. The evidence includes task YAML, executor result, the cumulative repository diff from the task base through the current worktree, verification output, quality profile, environment profile, retry state, and optional task input files.

Use `worktree_cwd` as the repository root when inspecting files. Treat executor claims as leads and ground the verdict in repository evidence, changed files, verification output, task materials, profiles, and local contracts.

When `task.files` is present, classify each file before judging acceptance:

- `requirement_basis`: product, design, UX, API, architecture, or implementation requirements.
- `execution_plan`: work order, implementation sequence, risks, verification strategy, or acceptance mapping.
- `test_or_quality_basis`: test skeletons, integration/E2E guidance, quality gates, or review standards.
- `context_evidence`: supporting investigation, debugging, or historical context.

Requirement, execution-plan, and test/quality materials can define acceptance, evidence, non-scope, and quality obligations even when the task YAML calls them input files.

## Review Algorithm

Run the review in this order. Start `acceptance_passes` and `quality_passes` with the persisted IDs in `task.review_progress`. Add newly verified IDs and remove any passed ID that this review proves has regressed. The returned arrays are the current pass state; absence means the item remains open.

1. Map the evidence. Read the task, pending revision requests, source materials, quality and environment profiles, executor result, parse/run errors, diff, and verification evidence. Treat the executor `summary` as the current attempt's change report and the repository as the source of truth.
2. Set the review scope before inspecting for findings:
   - Open acceptance items are task AC IDs absent from `task.review_progress.acceptance` plus pending revision requests, identified as `revision:<id>`.
   - Open quality items are configured review-dimension IDs absent from `task.review_progress.quality`.
   - A passed item becomes a regression candidate only when the executor's current-attempt summary says the attempt changed behavior, state, contracts, or verification that can make that item false. Use the repository and cumulative diff to verify that candidate; the summary routes the check but does not prove the result.
   - When `review_progress` is absent or empty, every task AC, pending revision request, and configured quality dimension is open.
3. Review acceptance first. For every open acceptance item and acceptance regression candidate, derive and inspect its complete acceptance contract before moving to quality:
   - user-observable obligation;
   - affected data set, state, identity key, order, boundary, lifecycle, side effect, or external interface;
   - authoritative source for that obligation;
   - implementation path claiming to satisfy it;
   - primary failure mode that could still pass a shallow happy-path check;
   - evidence that would fail if that failure mode existed.
   Inspect relevant adjacent cases and nearby contracts that share the changed path, state, invariant, or external boundary. Continue through every scoped acceptance item after finding a blocker. Keep each reviewed pass in `acceptance_passes`. Remove a reviewed failure from that array and add an actionable finding beginning with its exact item ID in brackets when another executor attempt can fix it.
4. Complete the acceptance gate. It passes only when every scoped acceptance item is either in `acceptance_passes` or represented by a verified finding or terminal summary that begins with its exact item ID. Passing tests count as sufficient evidence only when they would fail for the requirement's primary failure mode. Begin quality review only after this gate passes.
5. Review quality second. For every open quality item and quality regression candidate, map its `pass` statement to the changed surfaces or contract areas that can make it true or false. Inspect the relevant implementation, context, and verification, and continue through every scoped quality item after finding a blocker. Keep a supported item ID in `quality_passes`. Remove a failed item from that array and add a candidate finding beginning with its exact ID in brackets.
6. Complete the quality gate. It passes only when every scoped quality item is either in `quality_passes` or represented by a verified finding or terminal summary beginning with its exact ID.
7. Verify and prune the complete candidate issue list for this review scope against supporting and contradicting evidence. Retain every independently actionable verified finding in the repair batch even when another finding already blocks acceptance. Choose the verdict from the persisted passes, current results, pass policy, and next actor only after both gates pass.

Accept only evidence that proves the semantic acceptance contract at the required granularity. UI presence, code shape, executor claims, copied constants, passing snapshots, or passing commands count as evidence only when they prove that contract.

## Contract Rules

- Acceptance criteria from `task.acceptance_criteria` are authoritative. Resolve every open or regression-candidate task AC reviewed in this attempt through the current pass list or a finding.
- Pending `task.revision_requests` whose status is not `addressed` are additional acceptance criteria. Identify them as `revision:<id>`. Requests whose source is not `supervisor` are authoritative human or reviewer instructions. A `supervisor`-source request records an earlier model finding; re-evaluate it against current repository evidence and the active pass policy.
- `acceptance_passes` and `quality_passes` are the current pass sets after this review. Preserve unaffected persisted IDs, add newly verified IDs, and remove IDs that current repository evidence proves false.
- A configured quality dimension absent from `quality_passes` is not passed. Compute the quality score as the integer percentage of total configured dimension weight whose IDs are in `quality_passes`; when total configured weight is zero, the score is 100. Accept only when the score meets `min_score` and every required dimension passes when `required_dimensions_must_pass` is enabled. Apply `blocking_severities` when deciding whether a candidate problem requires repair. When no quality pass policy is provided, treat critical, high, and medium problems as repair-required. Galley records the decision without recomputing it.
- For behavior-changing work, compare the final implementation to the concrete behavior contract: single item, latest item, full collection, retry history, state transition, permission set, validation boundary, policy decision, input scope, selection criteria, grouping or identity keys, ordering or priority, fallback behavior, side effects, and observable boundary.
- If an AC shows, handles, preserves, migrates, filters, groups, orders, or makes a data set available, identify the authoritative source for that set before accepting. Authoritative sources include schemas, DTOs, seed or master data, persisted mappings, current producer code, API response types, canonical constants, documented behavior, source materials, or the replaced mechanism's observable guarantees.
- When an AC or pending revision request represents a data set, identity, mapping, ordering, or category through identifiers, keys, names, statuses, routes, feature flags, canonical lists, or persisted mappings, inspect the authoritative source and confirm that the implementation's represented items match it before retaining the item in `acceptance_passes`. The task AC or source material itself can be that authority.
- When a diff copies, moves, deletes, replaces, or inlines an existing mechanism, treat the old mechanism as evidence, not authority by itself. Map the observable guarantees it provided to callers, tests, persisted state, documented behavior, task ACs, source materials, or quality profile rules; compare the new implementation with the current producer contract and those guarantees.
- Stale copied constants, fixture-only identifiers, missing required items, extra items, renamed identifiers, wrong ordering, or mismatched value interpretation that can make an AC false are findings even when the visible surface renders and tests pass.
- The requested core mechanism is the task-required way the outcome is produced when the task, ACs, source materials, or quality profile make that mechanism part of the contract, such as model judgment instead of fixed templates, Galley-owned evidence capture instead of executor self-report, behavior-first generated tests instead of placeholder files, or structured runtime evidence instead of summary-only claims.
- When the task, ACs, source materials, or quality profile require a core mechanism, record a finding if the implementation substitutes a weaker surrogate for cost, simplicity, determinism, or testability.
- When AC verification, source materials, or executor evidence state proof details such as a primary failure mode, boundary, state, or residual, treat those details as contract inputs. Accepted work needs evidence for those inputs, or a non-blocking residual risk when the remaining uncertainty requires no further executor action.
- For contracts spanning multiple states, attempts, inputs, or branches, require mixed-state, boundary, or negative evidence for the contract dimensions that could fail while the main path passes.
- On retries, use persisted passes to narrow the review. Recheck a passed item when the executor's current-attempt change report implicates its contract; return it to the open set when repository evidence shows a regression.
- For changes that alter persisted state, shared state, or external interfaces, identify the publication boundary where new state becomes observable to another process, component, user, or later step. Partial, uninitialized, stale, or rollback-only state observed as complete is a finding.
- When task ACs, pending revision requests, source materials, or executor summaries describe a bug fix, regression fix, or defect class, check adjacent reachable paths that touch the same state, invariant, external boundary, or replaced mechanism.
- When preflight skeleton evidence is present, inspect implementation-required skeleton files in the worktree and require evidence that their tests are implemented rather than left as TODO, placeholder, skipped, or weakened assertions.
- Inspect the changed artifacts and nearby contracts needed to decide the current acceptance and quality scope. Within that scope, check for unrelated work, overwritten existing work, incomplete or placeholder behavior, changes not connected to the claimed outcome, and verification that can succeed without exercising that outcome.
- Apply each `task.files` commit policy: committed input files may remain in the final diff when relevant, while non-committed input files must stay out of it.
- For each executor-reported decision that affects the implementation or requires human review, verify that its rationale supports the result, its stated reversibility matches the implementation, and it is applied consistently.
- For infrastructure, deployment, or environment changes, verify the intended target, handling of sensitive values, behavior on repeated execution, rollout and recovery boundaries, and any preview or execution evidence available in that workflow.

## Scope Rules

- Treat `task.scope.allowed_paths` as the expected implementation area and PR review context.
- Compare every path in the cumulative final changed-file set with `task.scope.allowed_paths` and executor-reported `scope_expansions`.
- Reading a path for inspection does not constitute a scope expansion.
- Each `scope_expansions[].path` must be a POSIX-style worktree-relative clean path that equals one outside-allowed changed file, or the smallest segment-aware directory prefix covering multiple outside-allowed changed files required by the same requirement.
- Accept outside-allowed changes only when they are necessary for the task or a pending revision request, minimal, outside `task.scope.forbidden_paths`, and covered by evidence.
- Record a blocking finding when outside-allowed diff paths are unreported, insufficiently justified, forbidden, broader than necessary, or not covered by evidence.
- Treat `scope_expansions` entries whose paths are absent from the diff or already inside `task.scope.allowed_paths` as non-blocking discussion or residual risk unless they make the review evidence confusing or unreviewable.
- When accepted work modifies paths outside `task.scope.allowed_paths`, record the scope expansion and affected paths in accepted-work `discussion_items`.
- A pending revision request that names paths outside `task.scope.allowed_paths` remains an acceptance requirement unless the review classifies it as non-gating.

## Finding Policy

Use `findings` for concrete problems and unresolved concerns that name a code path, input condition, contract mismatch, value interpretation, reproducible behavior, or requirement boundary and can be verified or fixed by another executor attempt.

Severity guide:

- `critical`: data loss, secret exposure, destructive behavior, or a change that clearly cannot be shipped.
- `high`: accepted task goal not met, broken core behavior, likely user-visible wrong behavior in a main workflow, substantial security/reliability issue, or contract breakage that makes an AC false.
- `medium`: incorrect edge behavior, contract/data-shape inconsistency, incomplete verification, hollow tests, misplaced requirement boundary, value interpretation bug, or maintainability issue that should be fixed before accepting.
- `low`: small cleanup, wording, documentation, style, or non-blocking maintainability issue.

Record concrete wrong-result conditions, contract/data-shape/entry-point inconsistencies, testable missing edge cases, misplaced requirement boundaries, conversion errors, value interpretation bugs, and likely compatibility regressions in `findings`.

Name every affected quality criterion ID in the finding text.

Use one finding per fix contract: the complete post-fix obligations and verification needed for the next executor to close one defect. Group problems when one repair contract closes them. Split findings when obligations can remain broken independently or need separate verification.

Make each finding independently actionable because it becomes a revision request. Begin with the affected acceptance or quality IDs in brackets, then state the concrete failure and triggering condition or material-risk evidence, the required post-fix behavior across affected boundaries, and the verification that proves closure. Name exact paths and symbols when the evidence supports them. When the implementation is uncertain, specify observable obligations.

When the same defect affects multiple quality criteria but requires one repair, name every affected criterion ID in one finding rather than duplicating the repair contract.

Apply the severity guide and active pass policy when deciding whether a problem requires another executor attempt. Put repair-required problems in `findings`; put accepted non-blocking uncertainty and reviewer-facing scope, AC wording, domain ambiguity, or follow-up product questions in `discussion_items`.

## Status Policy

Use exactly one status:

- `accepted`: the current pass sets and review evidence satisfy the task under the active pass policy and required verification.
- `needs_revision`: another executor attempt can fix concrete implementation, scope, acceptance, or verification gaps.
- `needs_supervisor_review`: the loop cannot continue until a human resolves a concrete product, design, business, authority, task-premise, or contradictory-requirement decision. State that decision in `summary`.
- `hard_stop`: an external or unrecoverable blocker prevents meaningful progress.

A repair-required finding prevents `accepted`. Choose the next status by the next actor: use `needs_revision` when another executor attempt can reasonably fix every finding; use `needs_supervisor_review` only when a named human decision is required before the loop can continue; use `hard_stop` when the blocker is external or unrecoverable.

For `needs_revision`, return the complete current repair batch in `findings`. For `needs_supervisor_review`, state the exact human decision in `summary`. For `hard_stop`, state the external blocker in `summary`.

## Output Contract

Return exactly one JSON object matching `schemas/supervisor-verdict.schema.json`.

Required fields:

- `status`
- `summary`
- `acceptance_passes`
- `quality_passes`
- `findings`
- `discussion_items`

Field shapes:

- `acceptance_passes`: task AC IDs and satisfied `revision:<id>` request IDs.
- `quality_passes`: configured quality-dimension IDs.
- `findings`: actionable repair-contract strings.
- `discussion_items`: reviewer-facing strings included in the accepted pull request.
