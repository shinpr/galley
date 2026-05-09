# Setup

Use this reference when configuring Galley for a repository, daemon run, supervisor selection, or PR automation.

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
galley profile resolve --cwd <absolute-target-repo> --mkdir --output json
```

Advanced roots: use `--root <path>` only when the user intentionally runs a non-default daemon root. Task queueing normally discovers the running daemon queue automatically.

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

Repository profiles under the Galley root are loaded automatically from `scope.cwd`. Use explicit profile flags only when the user intentionally wants to override the conventional paths.

## PR Automation

Use PR automation when the task should finish with a reviewable branch:

```bash
galley daemon start \
  --open-pr \
  --poll-pr-comments \
  --reply-pr-comments
```

`--poll-pr-comments` enables `/galley rerun` or `/galley requeue` review feedback handling.
