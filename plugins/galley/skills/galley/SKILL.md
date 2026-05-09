---
name: galley
description: Use whenever the user mentions Galley, Galley task YAML, profiles, daemon, PR automation, run evidence, or troubleshooting. Provides workflows for creating/repairing implementation tasks, profiles, validation/queueing, daemon setup, and run diagnosis.
---

# Galley

Use this skill to turn a user's development intent into a valid Galley task, prepare a repository for Galley execution, or diagnose a Galley run.

## Context

Galley executes task YAML files through a file-backed queue. Task authoring is conversational; execution is AFK after queueing, meaning away-from-keyboard execution without further user interaction until completion, retry exhaustion, or supervisor escalation.

## Route (Read Only The Matching Reference)

- **Task authoring or repair**: read `references/task-authoring.md` and `references/authoring-quality.md`.
- **Quality or environment profile authoring**: read `references/profile-authoring.md`, `references/authoring-quality.md`, `references/quality.schema.json`, and `references/environment.schema.json`.
- **Validation, queueing, or handoff**: read `references/handoff-and-queueing.md`.
- **Install, setup, daemon, supervisor, or PR automation**: read `references/setup.md`, `references/profile-authoring.md`, `references/authoring-quality.md`, `references/quality.schema.json`, and `references/environment.schema.json`.
- **Codex CLI eval or daemon execution**: read `references/codex.md`.
- **Failed, stale, rejected, or confusing runs**: read `references/troubleshooting.md`.

## Core Flow

1. Use the route table to read the matching reference files.
2. For task, profile, or setup flows that create profiles, always load `references/authoring-quality.md` with the domain reference. For profile creation, load `references/quality.schema.json` and `references/environment.schema.json` before asking setup questions.
3. For new implementation tasks, ask one standalone first question for reference files. A complete reference-file answer has all three items: path/content, destination inside the execution workspace, and whether the file is included in the final commit. When the user already supplied all three, treat that as the checkpoint answer.
   Checkpoint: the user has answered with no reference files, or every supplied file has path/content, execution-workspace destination, and commit policy.
4. Read supplied planning/reference files only after their destination and commit policy are known; then carry those values into task direction and YAML.
   Checkpoint: every supplied file is represented as source, destination, and commit policy for `files[]`.
5. Use the current git repository as the target repo, or use an explicit repo path from the user; gather repository context after the reference-material gate.
   Checkpoint: the target repo path, dirty state, local guidance, affected paths, and verification signals are known.
6. Extract concrete requirements first, then apply `authoring-quality.md` question strategy.
7. Present task direction for user approval before discussing execution settings.
   Checkpoint: the user approves or adjusts goal, AC direction, scope, reference files, and quality basis.
8. Present execution settings separately with user-facing meanings and choices.
   Checkpoint: the user approves task YAML settings and understands current or planned daemon-owned settings.
9. For new task YAML, create the draft with the skill-bundled script `scripts/create_task_skeleton.py` from this Galley skill directory, then edit the generated skeleton; `galley task queue` places it into the active Galley queue after approval.
10. Validate using the handoff reference.
11. Queue using the handoff reference only after explicit user approval.
   Checkpoint: validation passed, queue target and daemon plan were shown, and the user approved queueing.

## Output

When authoring or repairing a task, report:

- task file path
- goal
- task essence
- acceptance criteria summary
- reference files used and commit policy
- scope and forbidden paths
- verification commands from profiles or repo docs, plus AC verification guidance
- runtime settings and daemon/PR automation plan
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
- Each acceptance criterion has an ID, text, and verification method or evidence source. Runnable checks belong in repo/profile verification commands when possible.
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
- `references/codex.md`
- `references/troubleshooting.md`
- `references/task.schema.json`
- `references/quality.schema.json`
- `references/environment.schema.json`
