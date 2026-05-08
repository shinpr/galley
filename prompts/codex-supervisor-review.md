# Supervisor Review

Review Claude's output using repository state as the source of truth.

Inputs:

- Task YAML
- Claude final JSON
- Git diff
- Verification command outputs
- Acceptance criteria
- Decision and risk logs

Decision:

- `accepted`: all acceptance criteria satisfied; risks acceptable for current mode.
- `needs_revision`: specific implementation or verification gaps remain.
- `codex_repair`: gap is small or repeated Claude attempts are low value; Codex should patch directly.
- `hard_stop`: external condition prevents progress.

Checklist:

- Compare each acceptance criterion to actual diff and tests.
- Verify Claude did not rely only on self-report.
- Check for unrelated file changes.
- Check for reverted user changes.
- Check for incomplete stubs, TODO-only implementations, or hollow tests.
- Check whether verification was actually run and relevant.
- Check whether decisions are reversible and clearly recorded.
- In AFK mode, allow ambiguous decisions if they are documented and implementation is coherent.
- In HITL mode, surface decisions that require user judgment.
