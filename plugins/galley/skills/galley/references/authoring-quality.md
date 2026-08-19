# Authoring Quality

Use these criteria to turn user intent and repository evidence into an executable Galley task. Keep only information that changes a decision, action, boundary, or verification result.

## Source Priority

Use sources in this order:

1. User request and supplied evidence
2. Repository instructions, accepted documents, code, tests, and CI
3. Existing Galley profiles and relevant prior run evidence
4. A focused user answer for a remaining blocking decision

Distinguish observed facts from inferences. Ask only when available evidence cannot resolve a decision that the executor is not authorized to make.

## Task Contract

### Outcome and Requirements

- Define one observable outcome.
- Keep only requirements that serve that outcome in the current task.
- Preserve user-stated exclusions and existing contracts affected by the change.
- Treat desired future work, possible reuse, optional hardening, and unrelated findings as out of scope unless the user makes them current requirements.
- Leave repository-local reversible implementation choices to the executor.

The task is not ready when execution would require a new product requirement or an unapproved durable choice that changes a public/shared contract, responsibility, dependency direction, persistence model, technology dependency, or irreversible data behavior.

### Impact and Edit Scope

Inspect enough evidence to identify:

- the responsibility that owns the requested behavior;
- the control, data, state, or rendering path that makes the outcome observable;
- affected callers or consumers when a shared contract changes;
- a representative repository pattern and applicable verification;
- an adjacent file required for the same outcome.

File count does not determine task complexity or scope. Stop expanding the search when more evidence cannot change the task contract or verification choice.

Set `allowed_paths` from the supported responsibility and expected verification files. Use `forbidden_paths` only for boundaries that require mechanical protection. Record a preserved behavior in an acceptance criterion or binding decision when path protection alone cannot express it.

### Acceptance Criteria

Write the smallest representative set that proves the outcome and any material boundary the change could break.

Each criterion must:

- state one observable obligation;
- contain exact public values when the request or existing contract fixes them;
- identify verification that can fail while the obligation is false;
- avoid prescribing an implementation shape unless that shape is itself required.

Add a separate compatibility, failure, authorization, persistence, or state criterion only when it protects a current requirement or an affected accepted contract. Discovery of another valid case does not by itself create an acceptance criterion.

### Verification

Choose the narrowest evidence that observes the criterion's boundary:

1. observable operation or output;
2. focused test;
3. build or static evidence when runtime proof is unavailable or unnecessary.

A wider check does not replace focused proof of the changed behavior. A focused check does not claim coverage of a wider boundary.

State a primary false-green condition only when a plausible implementation or test could pass while the criterion remains false. Add integration/E2E work, fixtures, screenshots, external services, or generated skeletons only when a cheaper check cannot observe the required boundary.

For behavior-changing criteria, add only proof details not already clear from the criterion, such as the primary false-green condition, evidence boundary, required state transition, or acceptance-relevant residual; record any residual in `risks`.

Use commands from the resolved quality/environment profiles, CI, package scripts, or repository documentation. Do not duplicate profile-owned required checks in every acceptance criterion; cite the focused evidence and let the profile enforce its repository checks.

## Reference Files

Copy a source into the worktree only when the executor needs its content and the task contract cannot carry the necessary facts more directly.

- Use `commit: false` for issue exports, plans, logs, screenshots, reviews, and other execution context.
- Use `commit: true` only when the user intends the supplied file to become repository output.
- Choose a safe destination within `allowed_paths`; ask only when destination or commit policy changes the intended repository result.

## Question Strategy

Ask after inspecting available evidence. A question is blocking only when its answer can change:

- the observable outcome or current requirement;
- a preserved public, shared, persistent, authorization, or irreversible boundary;
- the responsibility permitted to change;
- a user-owned external action or policy;
- whether the task can be verified safely.

Make and record low-risk reversible repository-local choices instead of returning them to the user.

## Completion Check

- [ ] One observable outcome owns every task obligation
- [ ] The executor does not need to invent a product requirement or durable design decision
- [ ] Scope follows the responsible path and preserves affected contracts
- [ ] Every acceptance criterion has falsifiable evidence at the required boundary
- [ ] Decisions, risks, files, and verification add only execution-relevant information
