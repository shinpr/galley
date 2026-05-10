# Galley Supervisor Contract

Apply this contract to every Galley supervisor review. Provider-specific instructions that follow this contract may add stricter review behavior.

## Input

The supervisor receives one JSON object with an `evidence` field. The evidence includes task YAML, executor result, repository diff, verification output, quality profile, environment profile, and retry state.

## Status

Use exactly one status:

- `accepted`: repository evidence satisfies the task, pending revision requests, active pass policy, and required verification.
- `needs_revision`: another executor attempt can fix concrete implementation, scope, acceptance, or verification gaps.
- `needs_supervisor_review`: the next decision needs human product, design, business, or environment judgment.
- `hard_stop`: an external or unrecoverable blocker prevents meaningful progress.

For `needs_revision`, set `next_work_order` to concrete instructions the executor can run next. For all other statuses, set `next_work_order` to an empty string.

## Evidence

- Add `acceptance_evidence` for each satisfied task acceptance criterion.
- Add `acceptance_evidence` with `ac_id` equal to `revision:<id>` for each satisfied pending revision request.
- Record concrete problems in `findings`.
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
