# Profile Authoring

Use this reference when creating or repairing Galley quality and environment profiles.

Profiles are repository-specific. Derive repository-owned settings from the codebase and ask only for policy choices that evidence cannot determine.

## Profile Types

| Profile | Purpose |
| --- | --- |
| quality | Defines required checks, review dimensions, evidence requirements, and pass policy. |
| environment | Defines cwd, available commands, optional setup commands for fresh worktree readiness, the implementation executor default, network/secrets/destructive-command constraints, PR behavior, PR comment handling, base branch, and worktree cleanup. |

The daemon resolves repository profiles from `scope.cwd` and the Galley root.

Backend defaults are intentionally separate:

- Implementation executor defaults: store `executor.default_cli`, `model`, and `effort` in `environment.yaml`. A task that sets `executor.cli` runs with the task's model and effort exactly as authored, with empty values delegating to that provider CLI; environment model and effort apply only when the task omits `executor.cli`, resolving each field from the task, then `environment.yaml`. When neither layer sets CLI, Galley uses `cli: claude`. When the resolved model is empty, the selected provider CLI chooses the model; when the resolved effort is empty, it chooses the reasoning effort. Pin task fields only when the user explicitly chooses them.
- Implementation executor effort: Claude and `glm` accept `low`, `medium`, `high`, `xhigh`, or `max`; Codex also accepts `minimal`; Grok accepts `none`, `minimal`, `low`, `medium`, `high`, `xhigh`, or `max`. Without `executor.default_cli`, profile validation accepts the provider union; task authoring validates the resolved CLI and effort before queue approval.
- Review supervisor default: optionally stored in `environment.yaml` as `supervisor.default_cli`; unset falls back to daemon startup state and then Claude.
- Review supervisor model: set `supervisor.model` to an exact provider model name; omit it or use an empty value to keep that CLI's default.
- Review supervisor effort: set `supervisor.effort` to `low`, `medium`, `high`, `xhigh`, or `max`; Codex also accepts `minimal`. Empty keeps the CLI default, and invalid provider values fail before review.
- Valid backends for either default are `claude`, `codex`, `glm`, `grok`, and `kimi`. Grok uses its installed, authenticated CLI. The supervisor is the acceptance gate, so its default is the user's choice; the daemon default is `claude`.

Use the bundled schemas as the profile field contract:

- `references/quality.schema.json`
- `references/environment.schema.json`

Read the schema for each profile being created or structurally repaired. Treat schema `default` values as fallbacks when repository evidence and user choices do not supply a value. User choices override schema defaults.

Validate profiles with:

```bash
galley profile validate --kind quality <profile.yaml>
galley profile validate --kind environment <profile.yaml>
```

Runtime loading ignores unknown profile keys while rejecting missing required keys and invalid known values.

## Authoring Flow

1. Resolve and read existing profiles. For an explicit field update, preserve unrelated fields, apply the requested values, validate, and stop.
2. For new or structurally invalid profiles, read the schemas and inspect README, CI, package scripts, setup documentation, and repository instructions.
3. Derive commands, setup, and review dimensions from repository evidence. Use schema defaults for remaining implementation defaults.
4. Ask one combined question only for unresolved user-owned policy that changes blocking severity, executor or supervisor choice, network/secret/destructive authority, or automatic PR behavior.
5. If profile creation was not already authorized, present the proposed values and ask once before writing. Otherwise write the requested profile directly.
6. Validate the changed profiles and report the evidence behind required checks and non-default policy.

## Repository Discovery

Explore only enough to identify candidate profile content:

- repository type, package/build system, and runtime
- documented setup, test, lint, typecheck, build, e2e, or release commands
- CI jobs and pre-commit/pre-push hooks that already define quality gates
- local services or tools required for realistic verification
- repo-local agent instructions, project skills, or contribution guidance
- existing Galley profile paths returned by `galley profile resolve --cwd <absolute-target-repo> --mkdir --output json`

Stop discovery when you can explain where each proposed required check or environment constraint came from. If current shell context is not the target repository, use the target repository's absolute path in commands.

## Profile Quality Rules

- Prefer repository-owned checks over generic best practices.
- Include a required check when it is referenced by CI, package scripts, contributor docs, or a user policy, or when it verifies a documented quality dimension for the task domain.
- Record why a check is required: CI usage, package script, existing docs, affected file type, or user policy.
- Present CI-derived checks as candidates before writing; CI evidence is strong, but required/blocking status is a policy choice.
- Separate "available command" from "blocking quality gate"; mark a runnable command as blocking only when it enforces an acceptance requirement, a CI gate, or a documented quality dimension for the task domain.
- Mark external resources as required only when the task domain needs them, such as Figma for UI work, DB services for persistence, or cloud/IaC tooling for infrastructure.
- Use `N/A` or omit fields for irrelevant domains; ask only about tools that match the task domain.

## Quality Profile Template

```yaml
id: "<repo-or-workflow-quality>"
required_checks:
  - id: "tests"
    preferred_commands:
      - "<test command>"
    required: true
review_dimensions:
  - id: "acceptance"
    weight: 5
    required: true
    pass: "Every acceptance criterion has implementation evidence or an explicit waived reason."
  - id: "regression"
    weight: 5
    required: true
    pass: "Existing behavior in touched paths remains compatible."
evidence_requirements:
  file_line_references: true
  command_outputs: true
pass_policy:
  required_dimensions_must_pass: true
  min_score: 85
  blocking_severities:
    - critical
    - high
    - medium
```

## Environment Profile Template

Include `setup` only when a stable fresh-worktree setup command is known.

```yaml
id: "<repo-or-workflow-environment>"
cwd: "/absolute/path/to/repo"
commands:
  test_unit: "<unit test command>"
  typecheck: "<typecheck command>"
  build: "<build command>"
setup:
  commands:
    - run: "<fresh worktree setup command>"
      why: "<why this prepares the worktree>"
executor:
  default_cli: "claude"
  # model: "<optional pinned model>"
  # effort: "high"
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

## Recommended Review Dimensions

Backend/API:

- `acceptance`: AC evidence exists.
- `regression`: touched behavior remains compatible.
- `api-contract`: schemas, handlers, clients, and docs agree.
- `data-integrity`: migrations, persistence, filtering, ordering, and limits are safe.
- `security`: auth, authorization, secrets, injection, and unsafe IO are reviewed.

Frontend/UI:

- `acceptance`: AC evidence exists.
- `regression`: existing flows remain compatible.
- `accessibility`: keyboard, labels, contrast, focus, and semantic structure are checked.
- `ui-ux`: layout, responsive behavior, text overflow, loading/error/empty states are reviewed.
- `visual-evidence`: screenshots or focused UI tests exist when visual behavior changed.

Infra:

- `acceptance`: AC evidence exists.
- `plan-safety`: plan/diff is reviewed before apply.
- `blast-radius`: affected resources and rollback are clear.
- `secrets`: credentials and state files are protected.

CLI/library:

- `acceptance`: AC evidence exists.
- `compatibility`: public flags, output, API types, and error behavior remain compatible.
- `docs`: user-facing examples or help text match behavior.

## Command Selection

Prefer commands already used by CI, README, package scripts, Makefile, justfile, or existing test docs. Use generic commands only when repo evidence is absent and the command is clearly supported by the toolchain.

Required checks are Galley-owned commands, not merely executor guidance. Set `environment.required_checks.shell` explicitly when repository checks require a specific shell; otherwise omit it and use Galley's default shell resolution. On non-Windows hosts, omit this field unless a repository check requires a specific shell.

For each required check, record why it is required:

```markdown
- tests: `<repo test command>` from CI, README, or existing test docs for the changed behavior.
- static-check: `<repo static check command>` from CI, README, or existing quality docs.
- accessibility: left out because this task has no UI surface.
```

## Profile Placement

Resolve the daemon root and repo-specific profile paths before writing profiles. Use the same target repository path that will be written to task `scope.cwd`; this path determines the repo key.

```bash
galley profile resolve --cwd <absolute-target-repo> --mkdir --output json
```

Write profiles to the returned `quality_profile_file` and `environment_profile_file` paths. `--mkdir` creates the parent directory; if it was omitted, create the returned parent directory before writing. The daemon loads those paths automatically for tasks whose `scope.cwd` resolves to the same repo key.

## Output

Report:

```markdown
Profiles written:
- quality: <path>
- environment: <path>

Required checks:
- <id>: <command>

Evidence used:
- <repo file or user answer>: <profile decision>

Intentionally left out:
- <check/resource>: <why it is not useful for this repo or task domain>

Blocking severities:
- <severity list>

Environment constraints:
- executor default: <claude|codex|glm|grok|kimi|unset>
- required check shell: <auto|sh|bash|cmd|powershell|pwsh>
- supervisor default: <claude|codex|glm|grok|kimi|unset>
- network: <value>
- secrets: <value>
- destructive commands: <value>

Validation:
- <command>: <passed/failed>
```
