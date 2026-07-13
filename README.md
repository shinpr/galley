![Galley blueprint](docs/assets/galley-blueprint.jpg)

# Galley

[![Claude Code](https://img.shields.io/badge/Claude%20Code-Plugin-purple)](https://claude.ai/code)
[![Codex CLI](https://img.shields.io/badge/Codex%20CLI-Supported-10a37f)](https://developers.openai.com/codex/cli)
[![Grok Build](https://img.shields.io/badge/Grok%20Build-Plugin-black)](https://x.ai/)
[![Agent Skills](https://img.shields.io/badge/Agent%20Skills-Spec%20Compliant-blue)](https://developers.openai.com/codex/skills/)
[![CI](https://github.com/shinpr/galley/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/shinpr/galley/actions/workflows/ci.yml)
[![GitHub Release](https://img.shields.io/github/v/release/shinpr/galley)](https://github.com/shinpr/galley/releases)
[![License: MIT](https://img.shields.io/github/license/shinpr/galley)](LICENSE)

Galley is a local runtime for supervised AI coding tasks.

It runs Claude Code, Codex, GLM, or Grok Build in a git worktree, records evidence for each attempt, and asks a supervisor model to accept, retry, or escalate the result before the work is treated as done.

Galley is for tasks where the output should be inspectable later: the request, scope, checks, diffs, and supervisor verdict stay on disk.

Galley builds Galley: roughly half of this repository's merged implementation PRs were created from Galley-managed task branches.

[Browse Galley-built PRs](https://github.com/shinpr/galley/pulls?q=is%3Apr+is%3Amerged+head%3Aagent)

## Quick Start

Use the Galley skill to set up each repository. It installs or verifies the CLI, prepares repository profiles, drafts valid task YAML, and queues tasks only after approval.

Claude Code:

```text
/plugin marketplace add shinpr/galley
/plugin install galley@galley-tools
/reload-plugins
/galley:galley Set up Galley for this repository.
```

Codex:

```sh
codex plugin marketplace add shinpr/galley
```

Then start or return to Codex, open the plugin picker, install `Galley`, and ask the skill to set up the repository:

```text
/plugins
$galley Set up Galley for this repository.
```

Grok Build:

```sh
grok plugin marketplace add shinpr/galley
grok plugin install galley --trust
```

Then start Grok Build and ask the Galley skill to set up the repository:

```text
/galley Set up Galley for this repository.
```

## First Task

After setup, start with a small, reviewable task in an existing repository.

Claude Code:

```text
/galley:galley Create a Galley task for a small bug fix or test improvement in this repository. Use the repository's normal checks, keep the scope narrow, and queue it after I approve.
```

Codex:

```text
$galley Create a Galley task for a small bug fix or test improvement in this repository. Use the repository's normal checks, keep the scope narrow, and queue it after I approve.
```

Grok Build:

```text
/galley Create a Galley task for a small bug fix or test improvement in this repository. Use the repository's normal checks, keep the scope narrow, and queue it after I approve.
```

The skill will inspect the repository, draft a task file, show the acceptance criteria and execution settings, and ask before queueing it.

## Skill Workflow

The Galley plugin packages one Agent Skill for Claude Code, Codex, and Grok Build. The skill is the recommended path for setup and task authoring because it handles the pieces that are easy to get wrong by hand:

- repository setup, CLI checks, and profile paths
- task YAML drafting and validation
- quality and environment profile authoring
- queueing only after explicit approval
- failed-run diagnosis and requeue guidance

For skills-compatible clients that do not use the Claude or Codex plugin manifests, copy or symlink this directory into the client's skill path:

```text
plugins/galley/skills/galley/
```

The standalone skill still expects the `galley` CLI on `PATH`. Some workflows also use `claude`, `codex`, `gh`, and `python3`.

## Why Galley

Interactive AI coding sessions work well for short tasks. They get harder to trust when the work becomes asynchronous, long-running, or review-heavy.

- **Asynchronous execution with review**: run coding work away from the main checkout, then gate completion through a supervisor verdict.
- **Git-visible changes**: executor work happens in a managed worktree, so normal git review still applies.
- **Structured evidence**: every run records command plans, executor output, git status, diffs, and supervisor results under the workflow root.
- **Retry and escalation**: tasks can retry while the loop budget remains, then escalate with recorded context when human judgment is needed.
- **Repository-specific policy**: profiles describe expected checks, command names, network and secrets policy, PR behavior, and cleanup rules.
- **Optional PR handoff**: accepted work can be committed, pushed, opened as a PR, and requeued from trusted PR comments.

## How It Works

```text
task YAML
    |
    | galley task queue
    v
queued task
    |
    | daemon claims task
    v
isolated git worktree
    |
    | Claude Code, Codex, GLM, or Grok implements
    v
run evidence + git diff
    |
    | supervisor review
    +--> accepted ---------> done or PR opened
    +--> needs revision ---> retry while budget remains
    +--> needs review -----> failed for human review
    +--> hard stop --------> failed
```

## Core Concepts

- **Task YAML**: trusted local input describing the goal, acceptance criteria, scope, executor, verification, worktree, and PR behavior. See [docs/task-yaml.md](docs/task-yaml.md).
- **Quality profile**: optional repository expectations for required checks, review dimensions, evidence, and pass policy. See [docs/profiles.md](docs/profiles.md).
- **Environment profile**: optional repository defaults for commands, executor CLI/model/effort, constraints, PR behavior, and cleanup. See [docs/profiles.md](docs/profiles.md).
- **Executor**: Claude Code, Codex, GLM, or Grok Build backend that implements the task. Each executor field resolves from the task, environment profile, then built-in defaults. GLM requires `glm_api_key`; Grok uses its logged-in CLI state.
- **Supervisor**: Claude, Codex, GLM, or Grok Build backend that reviews the result against acceptance criteria, required checks, and recorded evidence. It is the acceptance gate; the default is Claude.
- **Worktree**: isolated git checkout used for AFK execution so the source repository stays clean.
- **Evidence**: files under `runs/<run-id>/` that make each attempt auditable after the fact.

## Manual CLI Installation

Most users should start with the skill. Install the binary manually when scripting setup or debugging the CLI outside the skill workflow.

macOS and Linux:

```sh
curl -fsSL https://raw.githubusercontent.com/shinpr/galley/main/scripts/install.sh | sh
```

Windows PowerShell:

```powershell
Invoke-WebRequest https://raw.githubusercontent.com/shinpr/galley/main/scripts/install.ps1 -OutFile install.ps1 -UseBasicParsing
powershell -NoProfile -ExecutionPolicy Bypass -File .\install.ps1
```

Or use Go directly:

```sh
go install github.com/shinpr/galley/cmd/galley@latest
```

Installing only the binary does not create repository profiles or start a daemon for a project. After manual installation, run the setup skill for the repository you want Galley to manage.

Useful direct checks:

```sh
galley --help
galley daemon status --output json
galley daemon run --once
```

## Safety and Trust

Galley treats task YAML as trusted local execution input. A user or process that can write task YAML can choose the goal, scope, reference files, branch, worktree, and verification guidance used by the executor and supervisor.

Run Galley only for repositories and task authors you trust. Keep secrets out of task-accessible files, use worktrees and `scope.forbidden_paths`, and keep normal git review in the loop. PR comment requeueing is accepted only from the recorded PR author.

See [SECURITY.md](SECURITY.md) for reporting and operational trust boundaries.

## Documentation

- [Task YAML](docs/task-yaml.md): task fields, starter template, permissions, input files, worktree paths, and acceptance skeleton preflight.
- [Profiles](docs/profiles.md): quality and environment profile fields with examples.
- [Operations](docs/operations.md): daemon commands, queue layout, task commands, operational notes, and development checks.
- [Supervision](docs/supervision.md): supervisor verdicts, retry behavior, executor selection, and evidence expectations.
- [PR automation](docs/pr-automation.md): accepted-task commits, PR creation, PR comment requeueing, and worktree cleanup.

## Development

Galley is tested on Linux, macOS, and Windows in CI. Windows support is CI-covered, including daemon start, stop, and status paths; validate full local operation in your own Windows environment before relying on it for unattended work.

The `examples/` directory is for Galley checkout development and CI validation. Normal users should prefer `~/.galley` tasks created by the plugin skill.

```sh
go test ./...
go build ./cmd/galley
./scripts/smoke-local.sh
```

For local development and release notes, see [CONTRIBUTING.md](CONTRIBUTING.md) and [CHANGELOG.md](CHANGELOG.md).

## License

Galley is released under the MIT License. See [LICENSE](LICENSE).
