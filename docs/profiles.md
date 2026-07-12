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
- `required_checks[].preferred_commands[]`: ordered fallback commands for that check. Galley records the first passing command, or the last failure when all commands fail.
- `required_checks[].required`: whether missing evidence should count against acceptance.
- `review_dimensions[]`: repository-specific review dimensions such as acceptance, regression, security, or UI behavior.
- `review_dimensions[].weight`: non-negative relative weight for reporting.
- `review_dimensions[].required`: whether the dimension is mandatory.
- `review_dimensions[].pass`: observable pass condition for the dimension.
- When review dimensions are configured, the supervisor records `quality_coverage` for each criterion and changed-surface pairing, including the repository evidence inspected. Every configured dimension must appear, and findings categorized by dimension drive required-dimension and weighted-score policy.
- `evidence_requirements.file_line_references`: ask for file/line evidence in review output.
- `evidence_requirements.command_outputs`: ask for command output evidence.
- `pass_policy.required_dimensions_must_pass`: require all mandatory dimensions to pass.
- `pass_policy.min_score`: 0-100 threshold over all configured dimension weights. A dimension contributes its weight when no finding uses its ID as `category`; total configured weight of zero scores 100.
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
  default_cli: "claude"
supervisor:
  default_cli: "claude"
  model: "claude-sonnet-4-5"
  effort: "high"
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
- `executor.default_cli`: optional implementation executor default for new task authoring. Values are `claude`, `codex`, and `glm`. `glm` runs the `claude` binary against GLM's Z.ai endpoint and needs a `glm_api_key` in `daemon.yaml`. When it is unset, new task authoring uses Claude unless the author explicitly chooses another backend. An explicit task YAML `executor.cli` remains authoritative for that task.
- `supervisor.default_cli`: optional repository-scoped supervisor adapter. Values are `claude`, `codex`, and `glm`. When set, it overrides daemon startup supervisor settings for tasks in this repository.
- `supervisor.model`: optional model name passed unchanged to the selected Codex, Claude, or GLM supervisor CLI. Omit it or use an empty value to keep the CLI default; `runs/<run-id>/supervisor.json` records the effective setting.
- `supervisor.effort`: optional reasoning effort for the selected supervisor CLI. It reaches `claude` and `glm` as `--effort` and `codex` as the `model_reasoning_effort` config override. `claude` and `glm` accept `low`, `medium`, `high`, `xhigh`, or `max`; `codex` accepts `minimal`, `low`, `medium`, `high`, `xhigh`, or `max`. Omit it or use an empty value to keep the CLI default. An unsupported provider/effort combination fails before the supervisor subprocess starts; `runs/<run-id>/supervisor.json` records the effective setting and whether it came from this profile or the CLI default.
- `required_checks.shell`: optional shell for Galley-owned `quality.required_checks` execution. Values are `auto`, `sh`, `bash`, `cmd`, `powershell`, and `pwsh`.
- `required_checks.shell_path`: optional executable path override for required-check shell selection. When both `shell` and `shell_path` are set, `shell_path` wins.
- `constraints.network`: local network policy.
- `constraints.secrets_policy`: secret handling policy.
- `constraints.destructive_commands`: destructive command policy.
- `pr.enabled`: commit accepted changes, push the task branch, and open a PR.
- `pr.base`: base branch for opened PRs and new AFK task worktrees. See [pr-automation.md](pr-automation.md#base-branch-resolution) for start-point details.
- `pr.comments.enabled`: poll PR comments and accept trusted comments whose trimmed body starts with `/galley`. See [pr-automation.md](pr-automation.md#pr-comment-requeueing).
- `pr.comments.reply`: post a concise acknowledgement after handling a Galley PR comment.
- `worktree.cleanup`: remove managed task worktrees for closed or merged PR tasks, including uncommitted or generated files left in those worktrees.
- `setup.commands[]`: optional ordered setup plan used before acceptance skeleton preflight and implementation. Each entry has a `run` shell command and an optional `why`.

Validate an environment profile:

```sh
galley profile validate --kind environment ~/.galley/profiles/<repo-key>/environment.yaml
```

## Runtime Use

When profiles are loaded, Galley records them in `runs/<run-id>/attempt-N/profiles.json`.

Profiles shape work orders and supervisor review. They do not create a sandbox boundary by themselves; the task scope, worktree, shell environment, and local OS controls still define what can run.
