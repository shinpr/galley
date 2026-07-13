# Profile Authoring

Use this reference when creating or repairing Galley quality and environment profiles.

Profiles are repository-specific. Create them interactively because the right quality gates depend on the codebase, available tooling, runtime services, and user risk tolerance. Treat profile creation as quality-policy authoring, not a background setup detail.

Use `references/authoring-quality.md` to decide when to use existing repo standards, when to ask questions, and how to choose domain-specific gates.

## Profile Types

| Profile | Purpose |
| --- | --- |
| quality | Defines required checks, review dimensions, evidence requirements, and pass policy. |
| environment | Defines cwd, available commands, optional setup commands for fresh worktree readiness, the implementation executor default, network/secrets/destructive-command constraints, PR behavior, PR comment handling, base branch, and worktree cleanup. |

The daemon resolves repository profiles from `scope.cwd` and the Galley root.

Backend defaults are intentionally separate:

- Implementation executor defaults: store `executor.default_cli`, `model`, and `effort` in `environment.yaml`. Task fields override them independently; omitted fields use `cli: claude`, `effort: high`, and the CLI-default model. Pin task fields only when the user explicitly chooses them.
- Implementation executor effort: Claude and `glm` accept `low`, `medium`, `high`, `xhigh`, or `max`; Codex also accepts `minimal`; Grok accepts `none`, `minimal`, `low`, `medium`, `high`, `xhigh`, or `max`. Without `executor.default_cli`, profile validation accepts the provider union; task authoring validates the resolved CLI and effort before queue approval.
- Review supervisor default: optionally stored in `environment.yaml` as `supervisor.default_cli`; unset falls back to daemon startup state and then Claude.
- Review supervisor model: set `supervisor.model` to an exact provider model name; omit it or use an empty value to keep that CLI's default.
- Review supervisor effort: set `supervisor.effort` to `low`, `medium`, `high`, `xhigh`, or `max`; Codex also accepts `minimal`. Empty keeps the CLI default, and invalid provider values fail before review.
- Valid backends for either default are `claude`, `codex`, `glm`, and `grok`. Grok uses its installed, authenticated CLI. The supervisor is the acceptance gate, so its default is the user's choice; the daemon default is `claude`.

Use the bundled schemas as the profile field contract:

- `references/quality.schema.json`
- `references/environment.schema.json`

Read both schema files before profile intake. Treat schema `default` values as the recommended defaults when presenting setup choices. User choices override schema defaults.

Validate profiles with:

```bash
galley profile validate --kind quality <profile.yaml>
galley profile validate --kind environment <profile.yaml>
```

Runtime loading ignores unknown profile keys while rejecting missing required keys and invalid known values.

## Authoring Flow

1. Explain the two profiles before reading or writing them:
   - `quality.yaml` defines the checks, review dimensions, evidence, and severities that Galley uses to decide whether work is acceptable.
   - `environment.yaml` defines the repository cwd, runnable commands, optional setup commands for fresh worktree readiness, implementation executor default, network/secrets policy, services, destructive-operation constraints, PR behavior, and worktree cleanup.
2. Read `references/quality.schema.json` and `references/environment.schema.json` to establish fields, defaults, and valid shapes.
3. Ask the user to choose review strictness before repository inspection. This is an operating policy, not repository evidence.
4. Ask for the implementation executor choice before repository inspection. Ask for a review supervisor choice only when the repository should override daemon startup state. Use the backend default rule above for unset values.
5. Ask to inspect the repository for profile candidates. Mention the concrete sources you plan to read, such as README, CI, package scripts, Makefiles, justfiles, test docs, and existing local guidance.
6. Inspect the approved sources and draft candidate values from discovered evidence plus schema defaults and the chosen review strictness.
7. Present the proposed profile before writing files: required checks, optional checks, review dimensions, blocking severities, implementation executor default, setup commands when known, supervisor default when chosen, environment constraints, PR/base/comment/cleanup settings, and the evidence behind each choice.
8. Ask for approval and ask whether the user has additional repository-specific standards to enforce.
9. Write the profile YAML only after approval.
10. Validate the profile YAML and report the evidence used to choose each required check.

## Repository Discovery

After the user approves repository inspection, explore only enough to identify candidate profile content:

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

## Interactive Intake

Ask only the questions needed for the profile being authored.

Use this sequence:

1. Read the profile schemas.
2. Ask one question for review strictness.
3. Ask the implementation executor choice. Ask the review supervisor choice only when the repository should override daemon startup state. Use the backend default rule above for unset values.
4. Ask one question for repository inspection approval. Keep PR automation, base branch, and cleanup out of this question.
5. After inspection, present a single profile proposal using schema defaults, chosen review strictness, executor choice, optional supervisor default, and discovered repo evidence.
6. Ask for profile approval and additional repository-specific standards.
7. Ask follow-up questions only for choices that affect acceptance, execution safety, unavailable services, or repository-specific policy.

Review strictness question:

```markdown
Choose the review strictness for this repository profile before I inspect the repository:

- Minimum: block only critical findings. Use when only work that cannot function as requested should stop acceptance.
- Standard (recommended): block critical, high, and medium findings. Use for normal development where functional failures, user-visible regressions, contract mismatches, and major inconsistencies should block acceptance.
- Strict: block critical, high, medium, and low findings. Use when small technical-quality issues should also prevent acceptance.

Which strictness should this profile use?
```

Quality profile questions:

1. What kind of repository is this: backend, frontend, fullstack, CLI, infra, library, mobile, game, or other?
2. Which discovered checks are mandatory before acceptance, and which are optional: unit tests, integration tests, typecheck, lint, build, e2e, accessibility, visual regression, security scan, infra plan?
3. Which command should Galley prefer for each mandatory check when repo evidence shows multiple options?
4. Which review dimensions should block acceptance: correctness, regression, API contract, data integrity, security, performance, accessibility, UI/UX, maintainability, docs?
5. Which review strictness should map to blocking severities: minimum, standard, or strict?
6. What level of evidence is required: file/line references, command output excerpts, screenshots, PR links, logs?

Environment profile questions:

1. What is the target repo absolute path?
2. Which discovered commands are available and safe to run repeatedly?
3. Which implementation executor should runs use by default: `claude`, `codex`, `glm`, `grok`, or unset to use Galley's built-in Claude backend at run time?
4. Should this repository set a review supervisor default: `claude`, `codex`, `glm`, `grok`, or unset so daemon startup state decides?
5. Which discovered setup commands should prepare a fresh task worktree before implementation, if they are known?
6. Does the repo require local services: DB, Docker, Redis, browser, dev server, Figma MCP, Playwright, cloud CLI?
7. Is network access allowed, approval-gated, or unavailable?
8. What is the secret policy: never read `.env`, allow named test env vars, use local dummy credentials, or require human setup?
9. Which destructive operations are denied or approval-gated: DB reset, migrations, docker prune, file deletion, terraform apply?
10. Should accepted AFK implementation tasks open PRs by default, and what base branch should they target?
11. Should `/galley` PR comments be polled and acknowledged for reruns and revision requests?
12. Should Galley clean up its managed clean worktrees after PRs are closed or merged?

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
- executor default: <claude|codex|glm|grok|unset>
- required check shell: <auto|sh|bash|cmd|powershell|pwsh>
- supervisor default: <claude|codex|glm|grok|unset>
- network: <value>
- secrets: <value>
- destructive commands: <value>

Validation:
- <command>: <passed/failed>
```
