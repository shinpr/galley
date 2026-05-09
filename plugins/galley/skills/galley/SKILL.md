---
name: galley
description: Creates and repairs Galley task YAML, quality profiles, and environment profiles; validates ACs; queues approved AFK tasks; diagnoses failed runs; and sets up daemon workflows. Use when the user asks to create a Galley task, clarify goal or ACs, define quality gates, capture repo commands/tools, validate or queue a task file, troubleshoot a failed/stuck run, inspect Galley evidence, or configure Galley daemon/PR comment automation.
---

# Galley

Use this skill to turn a user's development intent into a valid Galley task, prepare a repository for Galley execution, or diagnose a Galley run.

## Context

Galley executes task YAML files through a file-backed queue. Task authoring is conversational; execution is AFK after queueing, meaning away-from-keyboard execution without further user interaction until completion, retry exhaustion, or supervisor escalation.

## Route (Read Only The Matching Reference)

- **Task authoring or repair**: read `references/task-authoring.md` and `references/authoring-quality.md`.
- **Quality or environment profile authoring**: read `references/profile-authoring.md` and `references/authoring-quality.md`.
- **Validation, queueing, or handoff**: read `references/handoff-and-queueing.md`.
- **Setup, daemon, supervisor, or PR automation**: read `references/setup.md`.
- **Failed, stale, rejected, or confusing runs**: read `references/troubleshooting.md`.

## Core Flow

1. Use the route table to read the matching reference files.
2. For task or profile authoring, always load `references/authoring-quality.md` with the domain reference.
3. Gather repository context and read supplied or already-existing planning/reference files when available before drafting.
4. Extract concrete requirements first, then apply `authoring-quality.md` question strategy.
5. Write the task file under the Galley daemon root, usually `~/.galley/tasks/draft/`.
6. Validate using the handoff reference.
7. Present the goal, acceptance criteria, scope, and verification plan for user approval.
8. Queue using the handoff reference only after explicit user approval.

## Output

When authoring or repairing a task, report:

- task file path
- goal
- task essence
- acceptance criteria summary
- reference files used and commit policy
- scope and forbidden paths
- verification commands
- quality/profile basis used
- decisions made while filling gaps
- validation result
- queue status, if the user approved queueing

When troubleshooting, report:

- task status and run ID
- latest attempt result
- blocking supervisor findings or executor failure
- relevant evidence files reviewed
- recommended next action

## Quality Checklist

- The task has one concrete goal and observable completion criteria.
- Each acceptance criterion has an ID, text, and verification command or evidence source.
- `scope.cwd` is an absolute path to the target repository.
- `scope.permission` follows the field guidance in `references/task-authoring.md`.
- `allowed_paths` is as narrow as the task permits.
- `forbidden_paths` protects secrets, git internals, generated caches, and unrelated assets.
- User-supplied reference files are represented in `files` with source, destination, and commit policy when they help execution.
- AFK tasks follow the reversible-decision and question strategy in `references/authoring-quality.md`.
- Validation passes before queueing.
- Queueing happens only after explicit user approval.

## References

- `references/task-authoring.md`
- `references/authoring-quality.md`
- `references/profile-authoring.md`
- `references/handoff-and-queueing.md`
- `references/setup.md`
- `references/troubleshooting.md`
