---
name: galley
description: Authors and validates Galley task YAML, profiles, daemon handoff, run evidence, and queueing. Use when the user asks to create/repair/queue a Galley task, configure profiles/daemon/PR automation, or diagnose a Galley run.
---

# Galley

Use this skill to turn a user's development intent into a valid Galley task, prepare a repository for Galley execution, or diagnose a Galley run.

## Context

Galley executes task YAML files through a file-backed queue. Task authoring is conversational; execution is AFK after queueing, meaning away-from-keyboard execution without further user interaction until completion, retry exhaustion, or supervisor escalation.

Supervisor = LLM-based review gate that returns acceptance or revision verdicts. Executor = the LLM that performs implementation work inside the task worktree. Claim TTL and heartbeat are daemon liveness signals for running tasks.

## Flow Catalog

Use this table to choose the active flow. Load every required file for that flow before acting. SKILL.md selects the route; the routed references own step-by-step procedure and output format.

| Flow | Use when | Required files | Core invariant |
| --- | --- | --- | --- |
| Task authoring | User wants a Galley task YAML, ACs, scope, queueing, or task repair. | `references/task-authoring.md`, `references/authoring-quality.md`; use `scripts/create_task_skeleton.py`; read `references/task.schema.json` only for validation shape errors. | The task gives the executor one complete outcome contract, uses existing defaults, and stops at the stage requested by the user. |
| Profile authoring | User wants `quality.yaml` or `environment.yaml`, or setup finds missing profiles. | `references/profile-authoring.md`; read the matching profile schema only for creation or structural repair. | Profiles come from repository evidence, schema defaults, and unresolved user policy; profiles validate before completion. |
| Setup / daemon | User wants installation, repository setup, daemon start/status/stop, supervisor choice, or PR automation setup. | `references/setup.md`; when profile creation is needed, switch to Profile authoring files. | Setup makes Galley usable for the target repo without requiring manual task YAML or daemon option knowledge; profile-owned behavior stays in profiles. |
| Handoff / queue | User wants validation, queueing, requeueing, archiving, or daemon handoff for an existing task. | `references/handoff-and-queueing.md` | Queue, requeue, and daemon actions require authority for their actual effects; a direct request supplies that authority unless the next action crosses the authority boundary below. |
| Troubleshooting | A Galley command fails, reports a warning or available update, or the user asks why a task or run failed, stalled, became stale, or looks confusing. | `references/troubleshooting.md` | Classify command output or diagnose from task state and run evidence before selecting a recovery action; distinguish advisory notices from failures. |
| Codex daemon/eval | Galley is being run from Codex CLI, especially sandboxed daemon execution or evals. | `references/codex.md` plus the active flow files. | Account for Codex sandbox and writable-root limits; background daemon startup is a handoff, not a monitoring loop. |

Supplemental references:

| Condition | Add file | Purpose |
| --- | --- | --- |
| Galley is running on Windows | `references/windows.md` | Use Windows installation and required-check shell guidance. |

## Global Invariants

- Use existing profiles and repository evidence before adding task-specific checks, settings, or questions.
- Ask only when missing information changes the task outcome, a protected boundary, a user-owned policy, or an external action that lacks authority.
- An action crosses the authority boundary when it uses a different target repository or base branch than the request or resolved profile, changes reviewed task or profile content beyond an explicitly requested revision, adds a remote or destructive effect not authorized by the request or resolved profile, or changes an explicitly selected executor, supervisor, queue root, or run mode.
- Treat a direct request to write a profile, queue a task, or start/restart a daemon as authority for that action. Ask again only when the next action crosses this boundary.
- Use bundled scripts and Galley commands for deterministic defaults and validation.
- Return after a successful background daemon start unless the user explicitly requested monitoring.
- Use the output contract from the routed reference.

## References

- `references/task-authoring.md`
- `references/authoring-quality.md`
- `references/profile-authoring.md`
- `references/handoff-and-queueing.md`
- `references/setup.md`
- `references/windows.md`
- `references/codex.md`
- `references/troubleshooting.md`
- `references/task.schema.json`
- `references/quality.schema.json`
- `references/environment.schema.json`
