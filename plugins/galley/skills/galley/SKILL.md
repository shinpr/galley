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
| Task authoring | User wants a Galley task YAML, ACs, scope, queueing, or task repair. | `references/task-authoring.md`, `references/authoring-quality.md`, `references/task.schema.json`, `scripts/create_task_skeleton.py` | User approves task direction, execution settings, and queueing; task YAML is generated from the skill skeleton and validates before queueing. |
| Profile authoring | User wants `quality.yaml` or `environment.yaml`, or setup finds missing profiles. | `references/profile-authoring.md`, `references/authoring-quality.md`, `references/quality.schema.json`, `references/environment.schema.json` | Review strictness is chosen before repository inspection; profiles come from schema defaults, repository evidence, and user policy; profiles validate before completion. |
| Setup / daemon | User wants installation, repository setup, daemon start/status/stop, supervisor choice, or PR automation setup. | `references/setup.md`; when profile creation is needed, switch to Profile authoring files. | Setup makes Galley usable for the target repo without requiring manual task YAML or daemon option knowledge; profile-owned behavior stays in profiles. |
| Handoff / queue | User wants validation, queueing, requeueing, archiving, or daemon handoff for an existing task. | `references/handoff-and-queueing.md` | Queue and requeue happen only after validation and explicit user approval; report task file, queue target, daemon state, and next action. |
| Troubleshooting | User asks why a task or run failed, stalled, became stale, or looks confusing. | `references/troubleshooting.md` | Diagnose from task state and run evidence before suggesting requeue; distinguish task failure from daemon or process failure. |
| Codex daemon/eval | Galley is being run from Codex CLI, especially sandboxed daemon execution or evals. | `references/codex.md` plus the active flow files. | Account for Codex sandbox and writable-root limits when starting daemons or creating sibling worktrees. |

Supplemental references:

| Condition | Add file | Purpose |
| --- | --- | --- |
| Galley required-check execution host is Windows | `references/windows.md` | Select `environment.required_checks.shell` and align required-check command syntax with that shell. |

## Global Invariants

- Prefer the skill-led path: keep ordinary authoring conversational and keep daemon options profile-owned.
- Use existing profiles and repository evidence before inventing checks, ACs, or command text.
- Keep task content, profile policy, daemon startup, and queue approval as separate user decisions.
- Validate generated task/profile YAML before queueing or reporting setup complete.
- Ask for user approval before writing profiles, queueing tasks, or starting/restarting daemon processes.
- Use the output templates from the routed references.

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
