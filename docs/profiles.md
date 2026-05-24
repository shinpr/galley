# Profiles

Profiles are optional repository-specific YAML files that help Galley and the supervisor interpret expected quality gates and local execution constraints.

The plugin skill can create profiles interactively. For manual setup, resolve the repository profile directory first:

```sh
galley profile resolve --cwd /path/to/repo --mkdir --output json
```

By default, profiles live under the daemon root:

```text
~/.galley/profiles/<repo-key>/
  quality.yaml
  environment.yaml
```

`<repo-key>` is generated from the repository path. Use `galley profile resolve` instead of guessing it.

The packaged Galley skill includes generated schema references for profile authoring:

- `plugins/galley/skills/galley/references/quality.schema.json`
- `plugins/galley/skills/galley/references/environment.schema.json`

These files are generated from the Go profile contracts with `galley schema generate`, and CI verifies them with `galley schema check`.

## quality.yaml

`quality.yaml` defines review expectations for a repository.

```yaml
id: "default"
required_checks:
  - id: "tests"
    preferred_commands:
      - "go test ./..."
    required: true
review_dimensions:
  - id: "acceptance"
    weight: 5
    required: true
    pass: "Every acceptance criterion has implementation evidence or an explicit waived reason."
evidence_requirements:
  file_line_references: true
  command_outputs: true
pass_policy:
  required_dimensions_must_pass: true
  min_score: 85
  unresolved_high_findings_allowed: 0
  blocking_severities:
    - critical
    - high
    - medium
```

Supported fields:

- `id`: profile identifier.
- `required_checks[]`: named checks Galley runs after an executor attempt, records as verification evidence, and asks the supervisor to evaluate.
- `required_checks[].preferred_commands[]`: ordered fallback commands suitable for that check in this repository and execution environment. Galley runs them with the shell selected by `environment.required_checks.shell` and records the first passing command, or the last failure when all commands fail.
- `required_checks[].required`: whether missing evidence should count against acceptance.
- `review_dimensions[]`: repository-specific review dimensions such as acceptance, regression, security, or UI behavior.
- `review_dimensions[].weight`: non-negative relative weight for reporting.
- `review_dimensions[].required`: whether the dimension is mandatory.
- `review_dimensions[].pass`: observable pass condition for the dimension.
- `evidence_requirements.file_line_references`: ask for file/line evidence in review output.
- `evidence_requirements.command_outputs`: ask for command output evidence.
- `pass_policy.required_dimensions_must_pass`: require all mandatory dimensions to pass.
- `pass_policy.min_score`: 0-100 score threshold.
- `pass_policy.unresolved_high_findings_allowed`: number of unresolved high findings allowed.
- `pass_policy.blocking_severities`: severities that block acceptance. Values are `critical`, `high`, `medium`, and `low`.

Validate a quality profile:

```sh
galley profile validate --kind quality ~/.galley/profiles/<repo-key>/quality.yaml
```

## environment.yaml

`environment.yaml` defines local commands and constraints for a repository.

```yaml
id: "local-dev"
cwd: "/path/to/repo"
commands:
  test_unit: "go test ./..."
  build: "go build ./cmd/galley"
executor:
  default_cli: "codex"
supervisor:
  default_cli: "codex"
required_checks:
  shell: "auto"
constraints:
  network: "approval_required"
  secrets_policy: "never_read_env_files"
  destructive_commands: "deny"
pr:
  enabled: true
  base: "main"
  comments:
    enabled: true
    reply: true
worktree:
  cleanup: true
```

Supported fields:

- `id`: profile identifier.
- `cwd`: absolute path to the repository this profile describes.
- `commands`: named local commands the executor and supervisor can reference.
- `executor.default_cli`: optional implementation executor default for new task authoring. Values are `claude` and `codex`. When it is unset, new task authoring uses Codex unless the author explicitly chooses another backend. An explicit task YAML `executor.cli` remains authoritative for that task.
- `supervisor.default_cli`: optional repository-scoped supervisor adapter. Values are `claude` and `codex`. When this field is set, the daemon uses it as the supervisor for any task whose `scope.cwd` resolves to this environment profile, overriding the daemon CLI `--supervisor` flag, the `supervisor` field in `daemon.yaml`, and the built-in default. When unset, the daemon uses the daemon-startup precedence chain (`--supervisor` → `daemon.yaml` → built-in default). The resolved supervisor and the layer that chose it are persisted to `runs/<run-id>/supervisor.json` as review evidence.
- `required_checks.shell`: optional shell for Galley-owned `quality.required_checks` execution. Values are `auto`, `sh`, `bash`, `cmd`, `powershell`, and `pwsh`. When unset or `auto`, macOS/Linux use `/bin/sh`; Windows uses standard Git for Windows Bash when discoverable and otherwise falls back to `cmd.exe`. Galley records the resolved shell in verification evidence.
- `required_checks.shell_path`: optional explicit executable path for the configured `required_checks.shell` kind. Use this for non-standard Bash, custom PowerShell, WSL-based setups, or Unix environments that need a pinned shell such as Nix or Homebrew Bash. The value is used verbatim and skips discovery; Galley does not infer whether the executable name matches the selected shell kind. Setting `shell_path` without a concrete `required_checks.shell` (or with `required_checks.shell: auto`) is a profile validation error.
- `constraints.network`: local network policy.
- `constraints.secrets_policy`: secret handling policy.
- `constraints.destructive_commands`: destructive command policy.
- `pr.enabled`: commit accepted changes, push the task branch, and open a PR.
- `pr.base`: base branch for opened PRs. The same value is also used as the start-point ref when Galley creates a brand-new task worktree for an AFK run, so a queued task no longer inherits whatever ref the source repository's HEAD happened to point at when the daemon claimed it. When the source repository has an `origin` remote the daemon runs `git fetch --no-tags --quiet origin <base>` first; on success it uses `refs/remotes/origin/<base>` as the start-point (matching the `gh pr create --base <base>` intent for AFK runs that ultimately push to origin). If the fetch fails the daemon refuses to fall back to a possibly stale `refs/remotes/origin/<base>` and instead fails the claimed task in the `workspace` phase with an error that names the source repository path, `pr.base`, and the failed fetch operation; `galley task show` surfaces this reason in the latest error fields. Origin-less local checkouts (smoke test, fresh clones without an origin remote) fall back to `refs/heads/<base>` so they keep working without a network. If `pr.base` is non-empty and neither the resolved origin ref nor `refs/heads/<base>` exists in the source repository, the daemon fails the claimed task with a descriptive error. When `pr.base` is empty the new branch falls back to the source repository's current HEAD, matching the previous behavior. Reused worktree paths and existing branches keep their current tip — the start point only applies when `git worktree add -b <branch>` is materializing the branch for the first time.
- `pr.comments.enabled`: poll PR comments and accept any comment whose trimmed body starts with `/galley`. The free-form prefix `/galley <request>` is treated as the request, and the aliases `/galley rerun ...` and `/galley requeue ...` remain backward compatible. Mid-line mentions or `/galley` lines that are not the first non-whitespace token of the comment are ignored. Trust boundary: a `/galley` command is accepted only when the comment author's login matches the PR author login recorded on the task (`pr.author_login`, persisted at PR creation time). Comments that fail this check are marked processed without requeueing; when `pr.comments.reply` is enabled, Galley posts a concise rejection reply. Task files without a recorded `pr.author_login` (older runs) fail closed.
- `pr.comments.reply`: post a concise acknowledgement after handling a Galley PR comment. Replies do not echo the user-supplied request body; the parsed request text is preserved on the requeued task as a `RevisionRequest` so the executor still receives the user's intent.
- `worktree.cleanup`: remove managed task worktrees for closed or merged PR tasks, including uncommitted or generated files left in those worktrees.

Validate an environment profile:

```sh
galley profile validate --kind environment ~/.galley/profiles/<repo-key>/environment.yaml
```

## Runtime Use

When profiles are loaded, Galley records them in `runs/<run-id>/attempt-N/profiles.json`.

Profiles shape work orders and supervisor review. They do not create a sandbox boundary by themselves; the task scope, worktree, shell environment, and local OS controls still define what can run.
