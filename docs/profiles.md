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
- `required_checks[]`: named checks the executor should prefer and the supervisor should expect.
- `required_checks[].preferred_commands[]`: commands suitable for that check in this repository.
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
- `constraints.network`: local network policy.
- `constraints.secrets_policy`: secret handling policy.
- `constraints.destructive_commands`: destructive command policy.
- `pr.enabled`: commit accepted changes, push the task branch, and open a PR.
- `pr.base`: base branch for opened PRs. The same value is also used as the start-point ref when Galley creates a brand-new task worktree for an AFK run, so a queued task no longer inherits whatever ref the source repository's HEAD happened to point at when the daemon claimed it. When the source repository has an `origin` remote the daemon runs `git fetch --no-tags --quiet origin <base>` first; on success it uses `refs/remotes/origin/<base>` as the start-point (matching the `gh pr create --base <base>` intent for AFK runs that ultimately push to origin). If the fetch fails the daemon refuses to fall back to a possibly stale `refs/remotes/origin/<base>` and instead fails the claimed task in the `workspace` phase with an error that names the source repository path, `pr.base`, and the failed fetch operation; `galley task show` surfaces this reason in the latest error fields. Origin-less local checkouts (smoke test, fresh clones without an origin remote) fall back to `refs/heads/<base>` so they keep working without a network. If `pr.base` is non-empty and neither the resolved origin ref nor `refs/heads/<base>` exists in the source repository, the daemon fails the claimed task with a descriptive error. When `pr.base` is empty the new branch falls back to the source repository's current HEAD, matching the previous behavior. Reused worktree paths and existing branches keep their current tip — the start point only applies when `git worktree add -b <branch>` is materializing the branch for the first time.
- `pr.comments.enabled`: poll PR comments and accept any comment whose trimmed body starts with `/galley`. The free-form prefix `/galley <request>` is treated as the request, and the aliases `/galley rerun ...` and `/galley requeue ...` remain backward compatible. Mid-line mentions or `/galley` lines that are not the first non-whitespace token of the comment are ignored.
- `pr.comments.reply`: post a concise acknowledgement after handling a Galley PR comment. Replies do not echo the user-supplied request body; the parsed request text is preserved on the requeued task as a `RevisionRequest` so the executor still receives the user's intent.
- `worktree.cleanup`: remove clean worktrees for closed or merged PR tasks.

Validate an environment profile:

```sh
galley profile validate --kind environment ~/.galley/profiles/<repo-key>/environment.yaml
```

## Runtime Use

When profiles are loaded, Galley records them in `runs/<run-id>/attempt-N/profiles.json`.

Profiles shape work orders and supervisor review. They do not create a sandbox boundary by themselves; the task scope, worktree, shell environment, and local OS controls still define what can run.
