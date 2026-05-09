# Profile Authoring

Use this reference when creating or repairing Galley quality and environment profiles.

Profiles are repository-specific. Create them interactively because the right quality gates depend on the codebase, available tooling, runtime services, and user risk tolerance.

Use `references/authoring-quality.md` to decide when to use existing repo standards, when to ask questions, and how to choose domain-specific gates.

## Profile Types

| Profile | Purpose | Override Flag |
| --- | --- | --- |
| quality | Defines required checks, review dimensions, evidence requirements, and pass policy. | `--quality-profile-file` |
| environment | Defines cwd, available commands, network/secrets/destructive-command constraints. | `--environment-profile-file` |

The daemon normally resolves repository profiles from `scope.cwd` and the Galley root. Use the flags above only when the user intentionally wants to override the conventional profile paths for a daemon run.

Validate profiles with:

```bash
galley profile validate --kind quality <profile.yaml>
galley profile validate --kind environment <profile.yaml>
```

## Authoring Flow

1. Inspect the repository for existing quality and environment signals.
2. Read existing profiles, manifests, CI, package scripts, Makefiles, justfiles, README, CONTRIBUTING, test docs, and local skills when present.
3. Draft profile values from discovered evidence before asking the user.
4. Ask only for missing policy choices, unavailable service details, risk tolerance, or commands that cannot be inferred.
5. Validate the profile YAML and report the evidence used to choose each required check.

## Repository Discovery

Run targeted discovery before questions:

```bash
cd <target-repo>
find . -maxdepth 3 \( -name package.json -o -name go.mod -o -name Cargo.toml -o -name pyproject.toml -o -name Makefile -o -name justfile -o -name README.md -o -name CONTRIBUTING.md \)
find . -maxdepth 4 \( -path "*/.github/workflows/*" -o -name "docker-compose*.yml" -o -name "playwright.config.*" -o -name "vite.config.*" -o -name "next.config.*" -o -name "terraform.tf" \) 2>/dev/null
find ~/.galley/profiles -maxdepth 3 -type f 2>/dev/null
```

Read only files relevant to the repository type and requested workflow. If the current shell is not already in the target repository, use the target repository's absolute path in commands instead of `$PWD`.

## Profile Quality Rules

- Prefer repository-owned checks over generic best practices.
- Include a required check only when it produces useful evidence for Galley acceptance.
- Record why a check is required: CI usage, package script, existing docs, affected file type, or user policy.
- Separate "available command" from "blocking quality gate"; not every runnable command should block acceptance.
- Mark external resources as required only when the task domain needs them, such as Figma for UI work, DB services for persistence, or cloud/IaC tooling for infrastructure.
- Use `N/A` or omit fields for irrelevant domains; ask only about tools that match the task domain.

## Interactive Intake

Ask only the questions needed for the profile being authored.

Quality profile questions:

1. What kind of repository is this: backend, frontend, fullstack, CLI, infra, library, mobile, game, or other?
2. Which discovered checks are mandatory before acceptance, and which are optional: unit tests, integration tests, typecheck, lint, build, e2e, accessibility, visual regression, security scan, infra plan?
3. Which command should Galley prefer for each mandatory check when repo evidence shows multiple options?
4. Which review dimensions should block acceptance: correctness, regression, API contract, data integrity, security, performance, accessibility, UI/UX, maintainability, docs?
5. Which severities block acceptance: critical/high/medium/low?
6. What level of evidence is required: file/line references, command output excerpts, screenshots, PR links, logs?

Environment profile questions:

1. What is the target repo absolute path?
2. Which discovered commands are available and safe to run repeatedly?
3. Does the repo require local services: DB, Docker, Redis, browser, dev server, Figma MCP, Playwright, cloud CLI?
4. Is network access allowed, approval-gated, or unavailable?
5. What is the secret policy: never read `.env`, allow named test env vars, use local dummy credentials, or require human setup?
6. Which destructive operations are denied or approval-gated: DB reset, migrations, docker prune, file deletion, terraform apply?

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
  unresolved_high_findings_allowed: 0
  blocking_severities:
    - critical
    - high
    - medium
```

## Environment Profile Template

```yaml
id: "<repo-or-workflow-environment>"
cwd: "/absolute/path/to/repo"
commands:
  test_unit: "<unit test command>"
  typecheck: "<typecheck command>"
  build: "<build command>"
constraints:
  network: "approval_required"
  secrets_policy: "never_read_env_files"
  destructive_commands: "deny"
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

For each required check, record why it is required:

```markdown
- tests: `pnpm test -- metadata-filter` from package.json scripts and changed TypeScript code path.
- typecheck: `pnpm run type-check` from CI workflow.
- accessibility: left out because this task has no UI surface.
```

## Profile Placement

Resolve the daemon root and repo-specific profile paths before writing profiles. Use the same target repository path that will be written to task `scope.cwd`; this path determines the repo key.

```bash
galley profile resolve --cwd <absolute-target-repo> --mkdir --output json
```

Write profiles to the returned `quality_profile_file` and `environment_profile_file` paths. `--mkdir` creates the parent directory; if it was omitted, create the returned parent directory before writing. The daemon loads those paths automatically for tasks whose `scope.cwd` resolves to the same repo key. CLI `--quality-profile-file` and `--environment-profile-file` are only overrides.

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
- network: <value>
- secrets: <value>
- destructive commands: <value>

Validation:
- <command>: <passed/failed>
```
