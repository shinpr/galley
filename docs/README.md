# Galley Documentation

Start with the main [README](../README.md) to install Galley and set up a repository with the Galley skill. The documents here explain how to configure, operate, and troubleshoot the resulting workflow.

## The Workflow

1. Use the Galley skill to inspect the repository and create its quality and environment profiles.
2. Ask the skill to draft a task, then review its acceptance criteria, scope, executor settings, and loop budget.
3. Queue the approved task. The daemon runs the executor in an isolated worktree.
4. A separately configured supervisor reviews acceptance criteria first, then repository quality policy. Verified passes carry forward so later attempts can focus on unresolved work and regression risks.
5. Accepted work is either left in the local worktree or committed and opened as a pull request, depending on the environment profile.

## Documentation Map

| Goal | Read |
| --- | --- |
| Choose an executor or supervisor, set model and effort, or understand retries | [Models and supervision](supervision.md) |
| Review or hand-write a task file | [Task YAML reference](task-yaml.md) |
| Understand or tune `quality.yaml` and `environment.yaml` | [Profiles](profiles.md) |
| Start and stop the daemon, inspect the queue, or configure notifications | [Operations](operations.md) |
| Understand a failed or stalled task and resume it safely | [Troubleshooting](troubleshooting.md) |
| Configure pull requests, comment requeues, and cleanup | [PR automation](pr-automation.md) |
| Understand security and trust boundaries | [Security policy](../SECURITY.md) |
| Build, test, or contribute to Galley | [Contributing](../CONTRIBUTING.md) |

## Configuration Files

Galley keeps settings at the scope where they apply:

| File | Scope | Contains |
| --- | --- | --- |
| Task YAML | One task | Goal, acceptance criteria, implementation scope, executor overrides, attempt budget, and worktree |
| `quality.yaml` | One repository | Required checks, review dimensions, evidence expectations, and pass policy |
| `environment.yaml` | One repository | Executor and supervisor defaults, commands, constraints, PR behavior, and worktree cleanup |
| `daemon.yaml` | One Galley daemon | Concurrency, polling, timeouts, daemon-level supervisor default, GLM credentials, and notifications |

The Galley skill is the recommended way to create task and profile files. The reference documents describe the resulting contracts for review, manual adjustment, and troubleshooting.
