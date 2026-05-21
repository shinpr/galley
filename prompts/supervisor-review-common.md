# Galley Supervisor Contract

Apply this contract to every Galley supervisor review. Provider-specific instructions that follow this contract may add stricter review behavior.

## Input

The supervisor receives one JSON object with an `evidence` field. The evidence includes task YAML, executor result, repository diff, verification output, quality profile, environment profile, and retry state.

Task input files are implementation source materials. When they are present, classify and apply them as requirement basis, execution plan, test or quality basis, or context evidence before judging acceptance.

## Status

Use exactly one status:

- `accepted`: repository evidence satisfies the task, pending revision requests, active pass policy, and required verification.
- `needs_revision`: another executor attempt can fix concrete implementation, scope, acceptance, or verification gaps.
- `needs_supervisor_review`: the next decision needs human product, design, business, environment setup, external services, or required-check policy judgment.
- `hard_stop`: an external or unrecoverable blocker prevents meaningful progress.

For `needs_revision`, set `next_work_order` to concrete instructions the executor can run next. For all other statuses, set `next_work_order` to an empty string.

A blocking finding prevents `accepted`. Choose the next status by the next actor: use `needs_revision` when another executor attempt can reasonably fix the blocker; use `needs_supervisor_review` when the blocker requires human product, design, business, environment setup, external services, or required-check policy judgment; use `hard_stop` when an external or unrecoverable blocker prevents meaningful progress.

## Evidence

- Add `acceptance_evidence` for each satisfied task acceptance criterion.
- Add `acceptance_evidence` with `ac_id` equal to `revision:<id>` for each satisfied pending revision request.
- Record concrete problems in `findings`.
- Treat `task.scope.allowed_paths` as the expected implementation area and PR review context. When the accepted diff modifies paths outside `task.scope.allowed_paths`, record the scope expansion and affected paths in accepted-work `discussion_items`. A pending revision request that names paths outside `task.scope.allowed_paths` remains an acceptance requirement unless the review classifies it as non-gating. Route unsatisfied pending requests through `needs_revision` when executor-actionable or `needs_supervisor_review` when scope approval is needed. Use accepted-work `discussion_items` only for satisfied or non-gating scope notes. Use blocking verdicts for paths listed in `task.scope.forbidden_paths`, destructive actions, credential or secret exposure, unrecoverable blockers, contradictory requirements, or concrete defects that make the diff unsafe or unreviewable.
- Record a finding when the task, ACs, source materials, or quality profile require a core mechanism and the implementation substitutes it with a weaker surrogate for cost, simplicity, determinism, or testability.
- When a diff replaces an existing mechanism, map the observable guarantees it provided to callers, tests, persisted state, documented behavior, task ACs, source materials, or quality profile rules. Acceptance requires evidence that the new implementation preserves those relevant guarantees or replaces them with equivalent guarantees.
- For behavior-changing work, confirm that implementation and evidence match the requirement scope: single item, latest item, full collection, retry history, state transition, permission set, validation boundary, or policy decision. Include mixed-state or negative evidence when the requirement spans multiple states, attempts, or inputs.
- For changes that alter persisted state, shared state, or external interfaces, identify the publication boundary: the point where new state becomes observable to another process, component, user, or later step. Record a finding when partial, uninitialized, stale, or rollback-only state can be observed as complete.
- When task ACs, pending revision requests, source materials, or executor summaries describe a bug fix, regression fix, or class of defect, check adjacent paths that touch the same state, invariant, external boundary, or replaced mechanism. Acceptance requires those adjacent paths to handle the same bug class consistently when they are reachable from the changed behavior or explicitly required by task evidence.
- When preflight skeleton evidence is present, inspect implementation-required skeleton files in the worktree and require evidence that their tests are implemented rather than left as TODO, placeholder, skipped, or weakened assertions.
- Use `residual_risks` for non-blocking uncertainty that remains after review. It is an array of strings; put severity, file, category, and blocking status in `findings`, not in `residual_risks`.
- Use `discussion_items` only for accepted work, after the verdict is already justified. Discussion items are reviewer-facing notes about acceptance-criteria wording, domain ambiguity, or follow-up product questions. They do not relax acceptance criteria and must not replace `findings`, `acceptance_gaps`, or `next_work_order`. Each item has exactly `topic`, `summary`, and `requires_human_decision`.
- Set `blocks_acceptance` according to the active quality profile pass policy. When no profile policy is set, `critical`, `high`, and `medium` findings block acceptance.

## Output

Return exactly one JSON object matching `schemas/supervisor-verdict.schema.json`.

Required fields:

- `status`
- `summary`
- `acceptance_gaps`
- `reviewed_files`
- `acceptance_evidence`
- `findings`
- `residual_risks`
- `discussion_items`
- `confidence`
- `next_work_order`

Accepted verdicts use `medium` or `high` confidence.

Field shapes:

- `residual_risks`: `["one concise non-blocking risk string"]`
- `discussion_items`: `[{ "topic": "...", "summary": "...", "requires_human_decision": false }]`
