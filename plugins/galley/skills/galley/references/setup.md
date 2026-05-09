# Setup

Use this reference when configuring Galley for a repository, daemon run, supervisor test, or PR automation.

## Preflight

Check required commands:

```bash
galley --help
claude --version
git status --short
```

If `galley` is missing, install it from a Galley checkout:

```bash
scripts/install.sh
```

or from a published module version:

```bash
GALLEY_VERSION=latest sh -c "$(curl -fsSL https://raw.githubusercontent.com/shinpr/galley/main/scripts/install.sh)"
```

The installer installs the `galley` binary. Daemon operations are available under `galley daemon ...`.

When PR automation is enabled, also check:

```bash
gh auth status
```

When Codex supervisor is enabled, check:

```bash
codex --version
```

## Repository Layout

Galley expects a daemon root. The default is `~/.galley`:

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
galley profile resolve --cwd <absolute-target-repo> --mkdir --output json
```

Use `--root <path>` with `profile resolve`, `task`, and `daemon` commands when the user intentionally runs an advanced non-default daemon root. Otherwise use the default `~/.galley` root.

## Supervisor Commands

Claude supervisor is the default:

```bash
galley daemon run --once
```

Codex supervisor:

```bash
galley daemon run --once --supervisor codex
```

Explicit Claude supervisor:

```bash
galley daemon run --once --supervisor claude
```

Use the same command shape for background mode by replacing `--once` with `start`.

Add profile context when the repository has profiles:

```bash
galley daemon run --once
```

## PR Automation

Use PR automation when the task should finish with a reviewable branch:

```bash
galley daemon start \
  --open-pr \
  --poll-pr-comments \
  --reply-pr-comments
```

`--poll-pr-comments` enables `/galley rerun` style review feedback handling when implemented by the repository version.

## Claude Plugin Test

Load the local plugin directly during development:

```bash
claude --plugin-dir ./plugins/galley
```

Validate the plugin if the installed Claude Code version exposes plugin validation:

```bash
claude plugin validate ./plugins/galley
```

If the command is unavailable, inspect `claude --help` and `/plugin` documentation for the installed version.

## Codex Plugin Test

Use the repo marketplace entry:

```bash
cat .agents/plugins/marketplace.json
```

If a Codex plugin validation command exists in the installed CLI, run it against `plugins/galley`. Otherwise validate the bundled skill with the local skill validator when available.
