# Setup

Use this reference when installing Galley, configuring a repository, starting the daemon, selecting a supervisor, or enabling PR automation.

## Setup Intake

Treat setup as a first-time Galley experience. Before asking the user to approve or choose a setup action, present the available options and explain the Galley-specific meaning of each option in plain terms. Use the detailed intake templates from the referenced flow when setup enters profile authoring, daemon startup, PR automation, or queueing decisions.

## Preflight

Check required commands and repository context:

```bash
galley --help
git status --short
```

Use the current git repository as the target repository when setup is requested from inside a repo. When the current directory is not a repository and the user did not provide a path, ask for the target repository before profile setup. Install-only requests can proceed without a target repository.

When `galley --help` fails, install the CLI as part of setup. For normal repository work, use the GitHub Release installer:

```bash
curl -fsSL https://raw.githubusercontent.com/shinpr/galley/main/scripts/install.sh | sh
```

Then verify the installed binary. Start with `galley --help`; when it is still not on `PATH`, use the path reported by the installer.

```bash
galley --help
```

Use a local checkout install only when working inside the Galley repository, when the user explicitly asks for a local build, or when the release installer is unavailable:

```bash
scripts/install.sh --local
```

Proceed with the standard installer when the user asks to install Galley, set it up, or make it available. Ask for a decision only when a required install choice is blocked, such as unavailable network access, an unwritable install destination, a requested non-default version or path, or a request to review commands before execution.

Use `galley` for later commands when it works on `PATH`; otherwise use the verified installed binary path. Continue with that verified binary path for the rest of setup. Inspect or edit shell startup files only when the user asks to make PATH persistent.

The installer installs the `galley` binary. By default it downloads a prebuilt GitHub Release asset; `--local` builds from the current checkout. Daemon operations are available under `<galley-bin> daemon ...`.

Check the selected provider CLI with `claude --version`, `codex --version`, or `grok --version`. GLM and Kimi use the Claude binary and require `glm_api_key` or `kimi_api_key` in `daemon.yaml`; Grok uses its normal logged-in CLI state. Check `gh auth status` when the accepted profile proposal enables PR automation.

## Repository Layout

Galley uses `~/.galley` by default:

```text
~/.galley/
  tasks/
    draft/
    queued/
    running/
    done/
    failed/
    archived/
  runs/
  profiles/
    <repo-key>/
      quality.yaml
      environment.yaml
```

Create directories through Galley commands when available. If creating them manually, keep task YAML files in `draft/` until validation and approval.

Resolve the repository-specific profile paths with the same target repository path that task YAML will use as `scope.cwd`:

```bash
<galley-bin> profile resolve --cwd <absolute-target-repo> --mkdir --output json
```

Use the returned JSON paths as the source of truth. Avoid broad `~/.galley` exploration during setup; inspect only the returned profile files or parent directory when confirmation is needed.

Advanced roots: use `--root <path>` only when the user intentionally runs a non-default daemon root. Task queueing normally discovers the running daemon queue automatically.

## Repository Profiles

Setup includes repository profiles. After resolving the profile paths:

- If `quality.yaml` or `environment.yaml` is missing, explain profiles and create them from this flow. Use `references/profile-authoring.md` and `references/authoring-quality.md` for deeper guidance when available.
- Profile creation starts by reading `references/quality.schema.json` and `references/environment.schema.json`; use schema defaults as the proposed values unless repo evidence or user choices point elsewhere.
- `quality.yaml` proposal includes required checks, review dimensions, evidence requirements, and blocking severities.
- `environment.yaml` proposal includes cwd, commands, optional setup commands for fresh worktree readiness, implementation executor default, optional required-check shell, network/secrets/destructive-command constraints, PR creation, PR comment handling, base branch, and worktree cleanup.
- The profile intake order is schema review, review strictness, implementation executor choice, supervisor choice, then repository inspection approval. Store the implementation executor choice in `environment.yaml` as `executor.default_cli`. The supervisor has two save targets — ask which the user wants and do not mix them: a repository default in `environment.yaml` `supervisor.default_cli` (overrides per task for this repo), or a daemon-wide choice applied at daemon start (`--supervisor` / `daemon.yaml`). PR automation is a profile proposal decision.
- Profile creation requires repository inspection approval and profile approval before writing files.
- Inspect the repository after approval, then draft candidate profiles from discovered commands, CI, README, config, and existing local guidance.
- Present one combined profile proposal after inspection. Include the evidence behind each required check and each environment setting.
- Ask for approval and additional repository-specific standards before writing profiles.
- Validate both profiles before reporting setup complete.

When setup needs to ask for both backend choices, use this shape during profile intake:

```markdown
Galley can use `claude`, `codex`, `glm`, `grok`, or `kimi` separately for implementation and review. Grok requires its installed, authenticated CLI. The supervisor is the acceptance gate, so its backend is the user's choice; the daemon default is `claude`.

- Implementation executor: writes the task changes in the worktree, stored in `environment.yaml` as `executor.default_cli`.
- Review supervisor: the acceptance gate. Its backend is saved either as a repository default (`environment.yaml` `supervisor.default_cli`) or for daemon startup only (`--supervisor` / `daemon.yaml`).

Answer each separately:

- Implementation executor: `claude`, `codex`, `glm`, `grok`, `kimi`, or unset (unset → Claude).
- Review supervisor backend: `claude`, `codex`, `glm`, `grok`, `kimi`, or unset (unset → Claude).
- Review supervisor save target: repository default (`supervisor.default_cli`) or daemon startup only.
```

```bash
<galley-bin> profile validate --kind quality <quality-profile-file>
<galley-bin> profile validate --kind environment <environment-profile-file>
```

## Daemon Commands

Choose daemon settings before startup. Explain the defaults and ask for changes when the user has not already chosen:

- implementation executor: store repository defaults in `environment.yaml`. A task that sets `executor.cli` runs with the task's model and effort exactly as authored, with empty values delegating to that provider CLI; environment model and effort apply only when the task omits `executor.cli`, resolving each field from the task, then `environment.yaml`. When neither layer sets CLI, Galley uses Claude. When the resolved model is empty, the selected provider CLI chooses the model; when the resolved effort is empty, it chooses its own reasoning-effort default.
- supervisor: Claude is the daemon default when unset; a non-default review backend from the list above can be selected at daemon start.
- PR automation, PR comment handling, base branch, and worktree cleanup: use the resolved `environment.yaml`.
- run mode: `daemon start` keeps working in the background; `daemon run --once` drains the current queue once.
- concurrency: keep defaults unless the user asks for parallel task execution.

Claude supervisor is the default:

```bash
<galley-bin> daemon start
```

Explicit Codex supervisor:

```bash
<galley-bin> daemon start --supervisor codex
```

Explicit Claude supervisor:

```bash
<galley-bin> daemon start --supervisor claude
```

For a single queue drain, use the same command shape with `run --once` instead of `start`.

Repository profiles under the Galley root are loaded automatically from `scope.cwd`.
Unknown `daemon.yaml` keys are ignored at runtime; invalid values for known keys still fail startup.
