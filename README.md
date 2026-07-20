![Galley blueprint](docs/assets/galley-blueprint.jpg)

# Galley

[![Claude Code](https://img.shields.io/badge/Claude%20Code-Plugin-purple)](https://claude.ai/code)
[![Codex CLI](https://img.shields.io/badge/Codex%20CLI-Supported-10a37f)](https://developers.openai.com/codex/cli)
[![GLM](https://img.shields.io/badge/GLM-Backend-2f6bff)](https://z.ai/)
[![Kimi](https://img.shields.io/badge/Kimi-Backend-000000)](https://www.kimi.com/code/en)
[![Grok Build](https://img.shields.io/badge/Grok%20Build-Plugin-black)](https://x.ai/)
[![Agent Skills](https://img.shields.io/badge/Agent%20Skills-Spec%20Compliant-blue)](https://developers.openai.com/codex/skills/)
[![CI](https://github.com/shinpr/galley/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/shinpr/galley/actions/workflows/ci.yml)
[![GitHub Release](https://img.shields.io/github/v/release/shinpr/galley)](https://github.com/shinpr/galley/releases)
[![License: MIT](https://img.shields.io/github/license/shinpr/galley)](LICENSE)

Galley is a local runtime for supervised, multi-model AI coding.

Choose the executor that fits the task and select the supervisor independently. Galley helps define acceptance criteria and repository policy, runs the work in an isolated git worktree, and records each attempt so the result can be inspected later.

This makes lower-cost, faster, or specialized models practical for implementation while keeping model configuration and acceptance criteria explicit. The recommended workflow hands accepted changes off as ordinary pull requests for final human review.

Galley builds Galley: roughly half of this repository's merged implementation PRs were created from Galley-managed task branches.

[Browse Galley-built PRs](https://github.com/shinpr/galley/pulls?q=is%3Apr+is%3Amerged+head%3Aagent)

## Why Galley

Interactive AI coding sessions work well for short tasks. Longer work needs explicit standards, visible model choices, and a reliable path back to human review.

- **Explicit model configuration**: set the executor CLI, model, and effort for a task or repository, and choose the supervisor separately. Galley records the resolved configuration passed to each run.
- **Repository-defined standards**: the skill inspects the repository, helps write acceptance criteria, and prepares quality and environment profiles before work is queued.
- **Focused retries**: the supervisor reviews acceptance criteria before repository quality policy, preserves verified passes, and focuses the next attempt on unresolved work and regression risks.
- **Auditable execution**: command plans, model output, checks, git status, diffs, and supervisor verdicts stay under the local workflow root.
- **Isolated AFK work**: execution runs in a managed git worktree, with retry limits and escalation when the task needs human judgment.
- **Review-ready handoff**: the recommended setup commits and opens accepted work as a pull request for final human review.

## Provider Requirements

The Galley plugin installs Galley tooling, not the CLIs used by executors and supervisors. Before repository setup, install and authenticate only the backends you plan to select.

| Backend | Required CLI | Setup |
| --- | --- | --- |
| Claude | [`claude`](https://code.claude.com/docs/en/setup) | Run `curl -fsSL https://claude.ai/install.sh \| bash` on macOS/Linux/WSL or `irm https://claude.ai/install.ps1 \| iex` in Windows PowerShell, then start `claude` and complete authentication. |
| Codex | [`codex`](https://developers.openai.com/codex/cli/) | Run `npm install -g @openai/codex`, then start `codex` and complete authentication. |
| GLM | [`claude`](https://code.claude.com/docs/en/setup) | Install Claude Code as above and create a [Z.AI API key](https://docs.z.ai/guides/overview/quick-start). |
| Kimi | [`claude`](https://code.claude.com/docs/en/setup) | Install Claude Code and complete Kimi's [Claude Code setup and API key creation](https://www.kimi.com/code/docs/en/third-party-tools/other-coding-agents). |
| Grok | [`grok`](https://docs.x.ai/build/overview) | Run `curl -fsSL https://x.ai/cli/install.sh \| bash` on macOS/Linux/WSL or `irm https://x.ai/cli/install.ps1 \| iex` in Windows PowerShell, then start `grok` and complete authentication. |

GLM and Kimi do not require separate provider CLIs. Galley runs both through Claude Code with the configured provider endpoint and API key. After installing Galley, create the default `~/.galley/daemon.yaml`:

```sh
galley daemon config init
```

Add the keys for the backends you use, then start the daemon:

```yaml
glm_api_key: "<your-Z.AI-api-key>"
kimi_api_key: "<your-Kimi-api-key>"
```

## Quick Start

Use the Galley skill to set up each repository. It installs or verifies the Galley CLI, checks the selected provider CLI, prepares repository profiles, drafts valid task YAML, and queues tasks only after approval.

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

## How It Works

```text
task YAML + repository policy
    |
    | galley task queue
    v
queued task
    |
    | daemon claims task
    v
isolated git worktree
    |
    | selected executor implements
    v
run evidence + git diff
    |
    | independently selected supervisor reviews
    +--> accepted ---------> PR opened or local completion
    +--> needs revision ---> retry unresolved work while budget remains
    +--> needs review -----> failed for human review
    +--> hard stop --------> failed
```

## Core Concepts

- **Task YAML**: trusted local input describing the goal, acceptance criteria, scope, executor overrides, verification, and worktree. See [docs/task-yaml.md](docs/task-yaml.md).
- **Quality profile**: repository review policy for required checks, review dimensions, evidence, and pass criteria. The setup skill creates it from repository evidence and the user's chosen review strictness. See [docs/profiles.md](docs/profiles.md).
- **Environment profile**: repository runtime defaults for commands, executor CLI/model/effort, constraints, PR behavior, and cleanup. The setup skill creates it from repository evidence and approved execution settings. See [docs/profiles.md](docs/profiles.md).
- **Executor**: Claude Code, Codex, GLM, Kimi, or Grok Build backend that implements the task. A task that selects its executor `cli` runs with the task's `model` and `effort` exactly as authored, with empty values deferring to that provider CLI's own defaults; otherwise each field resolves from the task, then the environment profile, and only `cli` has a built-in default (`claude`). GLM and Kimi use the Claude CLI and require `glm_api_key` or `kimi_api_key`; Grok uses its logged-in CLI state.
- **Supervisor**: Claude, Codex, GLM, Kimi, or Grok Build backend that reviews the result against acceptance criteria, required checks, and recorded evidence. It is the acceptance gate; the default is Claude.
- **Worktree**: isolated git checkout used for AFK execution so the source repository stays clean.
- **Evidence**: files under `runs/<run-id>/` that make each attempt auditable after the fact.

## Manual CLI Installation and Updates

Most users should start with the skill. Install the binary manually when scripting setup or debugging the CLI outside the skill workflow.

Run the installer for your platform for both the first installation and updates to the latest release.

macOS and Linux:

```sh
curl -fsSL https://raw.githubusercontent.com/shinpr/galley/main/scripts/install.sh | sh
```

Windows PowerShell:

```powershell
Invoke-WebRequest https://raw.githubusercontent.com/shinpr/galley/main/scripts/install.ps1 -OutFile install.ps1 -UseBasicParsing
powershell -NoProfile -ExecutionPolicy Bypass -File .\install.ps1
```

When updating, the installers stop a verified running daemon before replacing the binary but do not restart it. If the daemon was running, start it again after the update:

```sh
galley daemon start
```

Or use Go directly for the first installation:

```sh
go install github.com/shinpr/galley/cmd/galley@latest
```

After the first installation, create the default daemon configuration:

```sh
galley daemon config init
```

This creates `~/.galley/daemon.yaml` if it does not exist. It does not create repository profiles or start a daemon; run the setup skill for the repository you want Galley to manage.

Useful direct checks:

```sh
galley --version
galley --help
galley daemon status --output json
galley daemon run --once
```

## Safety and Trust

Galley treats task YAML as trusted local execution input. A user or process that can write task YAML can choose the goal, scope, reference files, branch, worktree, and verification guidance used by the executor and supervisor.

Run Galley only for repositories and task authors you trust. Keep secrets out of task-accessible files, use worktrees and `scope.forbidden_paths`, and keep normal git review in the loop. PR comment requeueing is accepted only from the recorded PR author.

See [SECURITY.md](SECURITY.md) for reporting and operational trust boundaries.

## Documentation

Start with the [documentation guide](docs/README.md) to follow the workflow or find a topic by goal.

- [Models and supervision](docs/supervision.md): executor and supervisor configuration, model roles, review convergence, and outcomes.
- [Troubleshooting](docs/troubleshooting.md): task states, recovery decisions, and run evidence.
- [Task YAML](docs/task-yaml.md) and [Profiles](docs/profiles.md): task and repository configuration references.
- [Operations](docs/operations.md): daemon control, queue storage, notifications, timeouts, and platform behavior.
- [PR automation](docs/pr-automation.md): accepted-task commits, PR creation, comment requeues, and worktree cleanup.

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
