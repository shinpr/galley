# Changelog

All notable changes to Galley are documented here.

This project follows semantic versioning.

## Unreleased

### Changed

- Packaged Claude and Codex Galley plugins are now versioned as `0.1.21`. General environment-profile examples and authoring guidance now use Claude as the default executor and supervisor backend.

## v0.9.2 - 2026-07-10

### Fixed

- `task queue` no longer fails when the daemon moves an existing task between state directories (e.g. queued to running) during duplicate-ID inspection; it rescans the current task state, still rejects genuine duplicate IDs, and only reports a bounded, source-preserving failure (distinct from a task execution failure) if it cannot obtain a stable view.

## v0.9.1 - 2026-07-10

### Fixed

- Claude executor and supervisor runs now pass Claude-compatible JSON schemas by stripping unsupported root `$schema` and schema combinators before invoking Claude Code.

## v0.9.0 - 2026-07-08

### Changed

- Added `glm` as an executor and supervisor backend. It runs the Claude Code binary against GLM's Anthropic-compatible endpoint (Z.ai) and is valid as `executor.cli` (implementation, setup, and acceptance-skeleton), `environment.yaml` `executor.default_cli`/`supervisor.default_cli`, and `--supervisor`. The Z.ai token is read from `daemon.yaml` `glm_api_key` and injected only into the child process environment; selecting `glm` without a token fails fast at daemon startup or before the executor runs.
- Packaged Claude and Codex Galley plugins are now versioned as `0.1.20`: task-authoring, setup, and profile guidance document the `glm` backend and its `glm_api_key` requirement, split the executor and supervisor questions, and separate the supervisor's repository default (`supervisor.default_cli`) from a daemon-startup-only choice; bundled task/environment schemas and skeleton generation now accept `glm`.

### Fixed

- Tasks queued with a `.yml` extension are now executed instead of silently ignored.
- `task list` and `task show` now resolve the running daemon's root like `queue`/`requeue`.
- Supervisor `needs_revision`/`hard_stop` verdicts are no longer rejected over a finding's `blocks_acceptance` flag.
- `scope.forbidden_paths` can no longer be bypassed by case on case-insensitive filesystems, and preflight ignores `.git` when detecting creator changes.
- Windows: daemon stop/verification and interrupted-task recovery now work, and child process trees are terminated on timeout/cancel.

## v0.8.4 - 2026-07-01

### Changed

- Supervisor review prompts now centralize acceptance-contract judgment in the shared contract, require authoritative-source evidence for represented identifier-backed data sets and mappings, and keep Claude/Codex provider prompts focused on runtime guidance.
- Removed brittle prompt-body section tests, keeping runner and adapter tests focused on prompt routing, embedding, and schema delivery.

## v0.8.3 - 2026-07-01

### Changed

- Treat `scope.allowed_paths` as expected implementation scope and review baseline instead of a hard stop. Executors may make minimal required outside-allowed changes, must still avoid `scope.forbidden_paths`, and must report those changes in `scope_expansions`.
- Removed `executor.max_budget_usd` from the active task contract and executor runners. Galley no longer emits Claude Code `--max-budget-usd`, and new task skeletons, schemas, examples, and docs no longer expose the field.
- Task YAML loading now decodes known fields and ignores unknown fields at runtime. Malformed YAML, incompatible top-level shape, and known-field type mismatches still fail validation, queueing, requeueing, and daemon execution.
- Executor result resolution now requires a valid structured executor JSON result from the normal executor output surfaces instead of synthesizing fallback completion evidence.
- Executor prompts now define `files_modified` as the final worktree changed-file set submitted for supervisor review, so `scope_expansions` coverage includes earlier-attempt changes still present in the current diff.
- Packaged Claude and Codex Galley plugins are now versioned as `0.1.19`: task-authoring guidance now treats `scope.allowed_paths` as expected implementation scope and supervisor review context while preserving `scope.forbidden_paths` as the protected path boundary; bundled task schemas and skeleton generation no longer expose `executor.max_budget_usd`.

### Fixed

- Codex executor result parse failures now keep the `--output-last-message` parse error in the final diagnostic instead of reporting only stdout parse failures.
- Fixed Galley review staging and accepted-task finalization failing when an executor had already staged a file deletion. Staged deletions are now preserved in review evidence and final commits without being re-added.

## v0.8.2 - 2026-06-27

### Changed

- Restored Claude as the unset default backend for both the implementation executor and the daemon supervisor. New task skeletons emit `executor.cli: claude` when neither `--executor-cli` nor `environment.yaml` `executor.default_cli` is set, and the daemon resolves the built-in supervisor default to `claude` when `--supervisor`, `daemon.yaml`, and repository `supervisor.default_cli` are all unset. Explicit `--executor-cli codex`, `executor.default_cli: codex`, `--supervisor codex`, and existing Codex task examples remain fully supported; schemas still accept both `claude` and `codex`.
- Packaged Claude and Codex Galley plugins are now versioned as `0.1.17`: task-authoring and profile guidance improves AC proof obligations, reference-file responsibility boundaries, and language-neutral examples.
- Runtime executor, setup, and acceptance-skeleton prompts improve AC proof-detail handling and language-neutral examples.

## v0.8.1 - 2026-06-24

### Fixed

- Daemon worktree cleanup now uses persisted final `pr.status` for already-final done tasks instead of refreshing PR state from GitHub, preventing historical tasks from causing recurring maintenance failures. Open PR tasks still refresh live state, and cleanup failures now include task, PR, and worktree context while the sweep continues to later tasks.

## v0.8.0 - 2026-06-23

### Added

- Added opt-in daemon notification command hooks. `daemon.yaml` now accepts `notifications.enabled`, `notifications.on`, and `notifications.command` to run an operator-owned command after terminal task statuses, defaulting to `failed` and `needs_supervisor_review`. Hook payloads are delivered via stdin JSON and `GALLEY_*` env vars, failures are best-effort with a fixed 30s timeout, and sample macOS/Slack scripts live under `docs/examples/notifications/`. Delivery is dispatched asynchronously so a slow or stuck notification command never delays the daemon worker or the next daemon iteration; daemon shutdown cancels in-flight notification commands, and `daemon run --once` waits for in-flight notification completion or timeout before normal exit.

## v0.7.7 - 2026-06-21

### Changed

- Supervisor review prompts now verify candidate findings against supporting and contrary repository evidence before turning them into revision work, while still recording concrete unresolved concerns that another executor attempt can act on.

## v0.7.6 - 2026-06-19

### Changed

- Task-authoring and executor guidance now preserves exact observable contract values when task text, acceptance criteria, input materials, public surfaces, tests, or authoritative existing examples make those values required.
- Packaged Claude and Codex Galley plugins are now versioned as `0.1.16`: bundled task-authoring guidance includes the observable contract value preservation rule.

## v0.7.5 - 2026-06-07

### Changed

- The normal daemon now runs PR comment polling and PR worktree cleanup independently from queued task execution, so long executor attempts no longer block `/galley` comment intake. `galley daemon run --once` is unchanged.
- Packaged Claude and Codex Galley plugins are now versioned as `0.1.15`: task-authoring acceptance-criteria guidance now classifies each item by obligation before keeping it as an AC, routing implementation-shape and out-of-scope items to `decisions` or `risks`, keeping a required outcome as an AC with strengthened verification instead of demoting it for weak verification text, and stating ACs as required outcomes rather than prohibitions on internal mechanisms; invariant expansion now also checks concurrent or reordered execution paths for new interleavings, races, double-processing, or lost updates.

## v0.7.4 - 2026-06-03

### Fixed

- Supervisor reviews now require evidence for acceptance-relevant boundary paths when a behavior-changing acceptance criterion could pass on the main path while failing a separate contract dimension.

## v0.7.3 - 2026-05-27

### Fixed

- Supervisor reviews now compare behavior-changing work against an explicit behavior contract, including source/reference behavior, implementation evidence, verification evidence, negative or mixed-state coverage, and retry drift, so accepted attempts are less likely to change observable behavior while still presenting passing checks.

## v0.7.2 - 2026-05-27

### Fixed

- Claude and Codex subprocesses now inherit the parent environment directly, fixing Windows runs where Galley's previous allowlist dropped required system and toolchain variables.
- Galley-owned git invocations now enable `core.longpaths=true`, avoiding Windows MAX_PATH failures during worktree cleanup, staging, and related git operations.
- Verification and setup output excerpts are now scrubbed to valid UTF-8 and truncated on rune boundaries, so accepted tasks with non-ASCII subprocess output no longer fail finalization with `cannot marshal invalid UTF-8 data as !!str`.

## v0.7.1 - 2026-05-26

### Changed

- Existing `environment.yaml` `setup.commands[]` plans now run through the setup executor instead of a daemon-owned direct execution path. Setup failures from saved plans can therefore be diagnosed and repaired in the same executor context that learns new setup plans, while successful repaired plans are still persisted back to `environment.yaml`.
- Packaged Claude and Codex Galley plugins are now versioned as `0.1.13`: profile-authoring guidance now mentions optional fresh-worktree setup commands when proposing `environment.yaml`, keeping detailed setup behavior in `docs/profiles.md` and the bundled environment schema.
- Packaged Claude and Codex Galley plugins are now versioned as `0.1.14`: Codex runtime guidance now tells agents to request elevated sandbox permissions before starting Galley daemons when the task repository or configured worktree path is outside the current Codex writable roots, and documents the `.git/FETCH_HEAD` and ref-lock permission errors that indicate sandbox-blocked daemon execution.

### Fixed

- Learned setup plans are now persisted only when the setup executor reports setup commands that actually exited successfully. Readiness-check-only commands such as tests or builds are no longer saved as `environment.setup.commands[]`, and failed setup executor results keep repair guidance visible in task/run evidence.

## v0.7.0 - 2026-05-25

### Added

- Setup executor preflight phase and `environment.yaml` `setup.commands[]`. Galley now prepares fresh task worktrees before acceptance skeleton creation and implementation by running authored setup commands or dispatching a setup executor to learn a reusable setup plan, which is persisted back to `environment.yaml` on success.
- Setup run evidence and failure routing. Setup now writes `setup_result.json` and, when a learned plan is persisted, `environment_update.json`; setup failures are classified as `phase: setup`, `kind: setup_failed` with repair guidance for `environment.setup`.

### Changed

- Packaged Claude and Codex Galley plugins are now versioned as `0.1.13`: setup executor prompts now include explicit result JSON contracts and troubleshooting guidance routes `setup_failed` diagnosis through setup run evidence.
- The daemon now stages the executor-produced reviewable path set before capturing supervisor diff evidence, so newly created untracked files appear in `diff.patch` and supervisor review while `commit:false` input files and unrelated context-only worktree dirtiness stay out of the submitted diff. Review staging writes `git_add_review.stdout.log`, `git_add_review.stderr.log`, and `git_add_review_result.json` attempt evidence when it runs, and records a skipped result when there is no reviewable path set.

### Fixed

- Review-time staging failures are now recorded as attempt errors with `phase=review_staging` / `kind=review_staging_failed`; Galley does not invoke the supervisor with an empty or stale diff and moves the task to `tasks/failed` with staging-specific evidence for operator diagnosis.

## v0.6.2 - 2026-05-25

### Changed

- `environment.yaml`'s `required_checks.shell_path` is now the explicit executable override for required checks. Recognized shell executable names can be used without `required_checks.shell`; when both fields are present, `shell_path` wins and `shell` is only fallback metadata for unrecognized executable names.
- Packaged Claude and Codex Galley plugins are now versioned as `0.1.12`: Windows profile guidance now treats `required_checks.shell_path` as the preferred exact executable override for non-standard shells, uses `required_checks.shell` only as fallback metadata for unrecognized executable names, and explains that explicit Windows Bash execution uses Git for Windows discovery unless operators opt into another Bash with `shell_path`.

### Fixed

- Windows required-check execution with `required_checks.shell: bash` now prefers standard Git for Windows Bash discovery and refuses to silently launch WSL, WindowsApps, MSYS2, Cygwin, Scoop, or other non-standard Bash entries. Operators can opt in to a specific non-standard Bash with `required_checks.shell_path`.
- Required-check evidence now records both the resolved shell kind and the executable path actually launched.

## v0.6.1 - 2026-05-25

### Changed

- Packaged Claude and Codex Galley plugins are now versioned as `0.1.11`: task-authoring guidance now drafts acceptance criteria value-first and enables acceptance skeleton preflight only when pre-created integration, cross-layer, or E2E skeletons add value, while Windows profile guidance and the bundled environment schema now document `required_checks.shell_path` for explicit non-standard shell executables and keep `auto` limited to standard Git for Windows Bash discovery.

### Fixed

- Windows required-check shell auto-discovery now ignores WSL launchers, WindowsApps shims, and non-standard Bash installs. Galley only auto-selects standard Git for Windows Bash, or Bash inferred from Git for Windows, and falls back to `cmd.exe` when no supported Git Bash is found.
- Acceptance skeleton preflight and task validation now allow multiple AC output entries to share one skeleton file path, preserving each entry's metadata while still enforcing path safety, allowed/forbidden scoping, declared-file checks, undeclared-change detection, and required AC coverage. Baseline hashes are deduplicated by slash-normalized path.
- PR comment polling now skips closed, merged, archived, PR-less, and non-open PR tasks before profile loading or GitHub calls while preserving requeue handling for open actionable PR tasks.

### Added

- `environment.yaml` now accepts `required_checks.shell_path` to explicitly choose the executable for a concrete `required_checks.shell` kind. This supports non-standard Bash, custom PowerShell, WSL-based setups, and pinned Unix shells; invalid `shell_path`/`auto` combinations are rejected, and verification evidence records the resolved shell executable.

## v0.6.0 - 2026-05-24

### Added

- Persistent daemon startup defaults in `daemon.yaml`. `galley daemon run` and `galley daemon start` now create `daemon.yaml` under the selected daemon root on first use with documented defaults for `supervisor`, `max_concurrent_tasks`, `max_concurrent_per_repo`, `poll_interval`, `claim_ttl`, `heartbeat_interval`, `shutdown_timeout`, and `idle_timeout`. Operators can edit the file to change daemon-wide defaults without re-specifying CLI flags. `galley daemon status` and `galley daemon stop` never create the file.
- Repository-scoped supervisor selection. `environment.yaml` now accepts `supervisor.default_cli` (`claude` or `codex`). When set, the daemon uses that supervisor for any task whose `scope.cwd` resolves to that environment profile, overriding the daemon startup supervisor for that task only. The resolved supervisor and the layer that selected it (`environment_profile`, `cli`, `daemon_config`, or `default`) are persisted to `runs/<run-id>/supervisor.json` as review evidence.

### Changed

- Daemon CLI flags on `galley daemon run` and `galley daemon start` always override the matching `daemon.yaml` field for that run. `--shutdown-timeout` now follows the same explicit-flag tracking as the other startup defaults.
- `galley daemon status` text and JSON output no longer report daemon startup-default fields that can be resolved per task or loaded from `daemon.yaml`. The removed JSON fields are `supervisor`, `max_concurrent_tasks`, and `max_concurrent_per_repo`; per-task supervisor evidence is recorded in `runs/<run-id>/supervisor.json`.
- Generated environment profile JSON Schema now declares the `supervisor.default_cli` enum (`claude`, `codex`). The bundled `plugins/galley/skills/galley/references/environment.schema.json` is regenerated.
- Packaged Claude and Codex Galley plugins are now versioned as `0.1.10`: task-authoring guidance now expands acceptance criteria by invariant before finalizing, checking sibling fields, later lifecycle states, stale or missing values, fallback paths, and publication or visibility boundaries so ACs cover acceptance-relevant edge cases without overloading a single observable obligation.

## v0.5.4 - 2026-05-22

### Changed

- Acceptance skeleton preflight now follows the task implementation executor backend: `executor.cli: codex` tasks run the built-in skeleton creator through Codex, while `executor.cli: claude` tasks keep the existing Claude creator path. The creator reuses task `executor.model` and `executor.effort`; daemon supervisor selection and persisted preflight evidence shape are unchanged, and no task YAML, profile, or CLI surface changed.
- Packaged Claude and Codex Galley plugins are now versioned as `0.1.9`: task-authoring guidance now explains that acceptance skeleton preflight follows the task executor backend, model, and effort, and the Codex skeleton creator prompt is tightened around Codex-style flow, output discipline, skeleton-only test obligations, and creator output declarations.
- Exhausted built-in supervisor idle-output watchdog failures are now reported as `supervisor_idle_timeout` with supervisor name, idle-timeout duration, and try count in failed task details, daemon logs, and `galley task show`. This only changes reporting; supervisor retry behavior and final failed task state are unchanged.
- Supervisor reviews now check that behavior-changing implementations and verification evidence match the requirement scope, including full collections, retry history, state transitions, permission sets, validation boundaries, and policy decisions, with mixed-state or negative evidence when a requirement spans multiple states, attempts, or inputs.
- Daemon startup now defaults the built-in supervisor adapter to Codex when `--supervisor` is unset. Operators can still select Claude explicitly with `--supervisor claude`; implementation executor defaults and task YAML `executor.cli` resolution are unchanged.

## v0.5.3 - 2026-05-19

### Fixed

- Legacy or historical task YAML files are no longer fatal for read-only inspection. `galley task list` and `galley task show <ID>` now scan task files through a lenient loader that tolerates unknown fields left over from earlier task schema revisions; an unreadable file surfaces as a non-fatal entry with `status: decode_error` (text) or `decode_error` (JSON) instead of aborting the whole command. The daemon's PR comment polling and worktree cleanup sweeps skip unreadable historical tasks with an operator-visible "skipping ... unreadable task" warning to stderr and continue processing the remaining readable tasks. `galley task archive` can now archive a legacy task file: when strict load fails because of unknown fields but the document still parses as a YAML mapping, archive performs a `yaml.Node` round-trip that only updates the top-level `status` field and retains unknown fields without normalizing the file to the current schema; when even safe status editing is unsafe, archive moves the file unchanged. Both fallback paths return a populated `ArchiveResult.Mode` (`legacy_status_edit` or `legacy_move_unchanged`) plus an explanatory `ArchiveResult.Warning`, and the `galley task archive` text output now echoes both so operators see why the legacy file was archived through the fallback path. Active task intake (`galley task validate`, `galley task queue`, `galley task requeue`) and daemon execution of a queued task continue to require current-schema task YAML through the strict loader.

## v0.5.2 - 2026-05-18

### Fixed

- Windows required quality checks now choose a more useful shell for Galley-owned verification. Repository `environment.yaml` profiles can set `required_checks.shell` to `auto`, `sh`, `bash`, `cmd`, `powershell`, or `pwsh`; when unset or `auto`, Windows uses Git Bash when `bash.exe` is discoverable and falls back to `cmd.exe`, while macOS/Linux keep `/bin/sh`. Verification evidence records the resolved shell and cmd.exe failures that look like POSIX-tool mismatches now point operators to `environment.required_checks.shell`.
- Windows RestrictedEnv inheritance for Codex executor, Codex supervisor, Claude supervisor, and acceptance skeleton creator subprocesses. The restricted environment used by these model subprocesses now preserves the Windows process environment keys that cmd.exe, `.cmd` shims, user-local tool discovery, temp files, and executable resolution require (`SYSTEMROOT`, `WINDIR`, `COMSPEC`, `PATHEXT`, `USERPROFILE`, `APPDATA`, `LOCALAPPDATA`, `TEMP`, `TMP`), and matches Windows env keys case-insensitively so parent environments using casings such as `Path`, `SystemRoot`, or `ComSpec` are preserved correctly. macOS and Linux RestrictedEnv behavior is unchanged: the existing Unix-oriented allowlist, `LC_*` preservation, and caller-supplied extra entries continue to work without inheriting unrelated parent environment variables. Claude executor full parent-env inheritance is unchanged. No task YAML, profile, or CLI surface changed.
- Windows command-line length failures in Claude executor/supervisor runs, acceptance skeleton creation, `git add` staging, and required quality checks. On Windows, Galley now routes generated prompts, pathspec lists, and verification command bodies through files or stdin instead of argv. macOS and Linux keep their existing command shapes. No task YAML, profile, or CLI surface changed.

### Changed

- Windows manual CLI installation now includes a native PowerShell installer, and Windows CI smokes both the PowerShell release installer and the Git Bash local installer path.
- Packaged Claude and Codex Galley plugins are now versioned as `0.1.8`: setup, profile-authoring, task-authoring, and queueing guidance now covers Windows required-check shell selection and native Windows installation while keeping Windows-specific guidance in the routed Windows reference.

## v0.5.1 - 2026-05-17

### Fixed

- Windows compatibility regressions reported in GitHub issue #41. Task path validation now compares logical (slash) cleaned forms across `worktree.path`, `scope.allowed_paths`, `scope.forbidden_paths`, `files[].source`, `files[].destination`, and `preflight.acceptance_skeleton` outputs, so YAML authored with `/` separators no longer silently passes parent-traversal checks on Windows (where `filepath.Clean` rewrites to `\`) and same-root sibling matches such as `internal-task` against `internal` no longer leak through containment. Task queueing file moves no longer rely on `os.Link` (which surfaced as a raw "not supported by windows" error on filesystems without hardlink support); the no-overwrite write path now publishes through a reservation lock plus same-directory temp-file-and-rename, so the final task YAML appears to a concurrently polling daemon only after its contents are fully written and synced. Duplicate-destination protection in `task queue`, `task requeue`, archive, and daemon claim/requeue is preserved.

### Changed

- Supervisor reviews now include a common regression-review rule for mechanism replacements: reviewers map observable guarantees from prior behavior to the new implementation, check publication boundaries for shared or persisted state, and inspect adjacent reachable paths when task evidence describes a bug class fix.
- Background daemon control now has an explicit Windows implementation path. `Alive` uses `OpenProcess` + `GetExitCodeProcess` instead of `signal(0)` (which is not supported on Windows), and `Stop` uses `TerminateProcess` because Windows has no SIGTERM equivalent that can be delivered to a console-less background daemon. The Windows `daemon start`/`stop` path is therefore an immediate termination rather than a graceful shutdown; operators that need a graceful stop on Windows should run `galley daemon run` in the foreground and use Ctrl+C. Foreground daemon run behavior is unchanged on every OS. `galley daemon start` post-spawn cleanup (after a failed PID file write or readiness probe) now goes through a cross-platform `TerminateChildProcess` helper that sends SIGTERM on Unix and calls `TerminateProcess` on Windows, so a failed background start no longer leaves a stranded daemon process on Windows. Child process-group cleanup still falls back to PID-level termination on Windows because the runner only creates Unix process groups via `Setpgid`. No new CLI flag, task YAML field, or environment profile field is introduced; in particular, `supervisor` remains daemon startup state (the `--supervisor` flag on `galley daemon run`/`start`) and is not added to `environment.yaml`.

## v0.5.0 - 2026-05-15

### Changed

- Repository `environment.yaml` profiles now support an optional `executor.default_cli` for new task authoring. The task skeleton generator uses that repository default when present and now falls back to Codex when it is unset, while explicit task YAML `executor.cli` remains authoritative for existing tasks. Setup and task-authoring guidance now make the implementation executor choice explicit because Claude Code print-mode usage moved to Agent SDK billing, which changes the cost profile of the previous Claude-oriented default.
- Task YAML `executor.max_budget_usd` is now optional in the schema, and new Codex task skeletons omit it because `codex exec` does not expose a matching budget flag. Explicit values remain valid for existing tasks and continue to drive the Claude CLI budget flag.
- Packaged Claude and Codex Galley plugins are now versioned as `0.1.7`: setup, profile-authoring, task-authoring, and queueing guidance now distinguish implementation executor defaults from daemon supervisor selection, resolve repository executor defaults from `environment.yaml`, and document `/galley` PR comment requeue behavior.

## v0.4.0 - 2026-05-14

### Added

- Task YAML `executor.cli` now accepts `codex` in addition to `claude`. The daemon dispatches implementation attempts through the selected binary (`ClaudeBin` for `claude`, `CodexBin` for `codex`), persists per-attempt evidence under the same `runs/<id>/attempt-N/` layout as the Claude path, records the executor command used (`claude -p` or `codex exec`) in `task.verification.commands`, and classifies executor success, retry, and idle-timeout outcomes consistently across providers. The Codex command plan is built to match the `codex exec` CLI surface: reasoning effort is delivered via `-c model_reasoning_effort="<value>"` because `codex exec` does not expose `--effort`, `executor.prompt_mode=append` is recorded as an informational warning because Codex receives one concatenated stdin prompt, and `executor.max_budget_usd` is recorded as an informational warning because Codex has no equivalent flag. The Codex executor now uses a Codex-specific executor prompt that preserves the same task/result contract as the Claude executor prompt. Generated schemas, the skill-bundled `task.schema.json` reference, the executor-selection guidance in `docs/task-yaml.md`, and the new `examples/afk-task-codex.yaml` describe the selection surface.

### Changed

- Executor attempt evidence now writes structured executor output to `executor_result.json`. Galley still reads legacy `claude_result.json` files for existing run evidence, but new runs no longer write the legacy filename.
- Task YAML `executor.model` is now optional. When omitted, Galley lets the selected executor CLI use its configured default model; pinned model names remain supported for tasks that need an explicit override.
- Skill-bundled `scripts/create_task_skeleton.py` now includes the optional acceptance skeleton preflight stage in new task YAML with `enabled: false`. This makes the opt-in preflight setting visible while preserving the existing disabled behavior; enabled-only fields such as `mode`, `required`, `allowed_paths`, and `outputs` remain omitted until an author enables the stage.
- Packaged Claude and Codex Galley plugins are now versioned as `0.1.6`: task authoring omits `executor.model` by default, preserves explicit model overrides only when the user requests one, includes Codex executor selection in the bundled task schema, diagnoses new `executor_result.json` run evidence, and keeps setup/install guidance provider-neutral for fresh worktrees.

## v0.3.5 - 2026-05-13

### Fixed

- Daemon finalize no longer drops the first character of the first changed path when staging the accepted diff. `workspace.gitOutput` now strips only the trailing newline of captured git output instead of all surrounding whitespace, preserving the leading space that `git status --porcelain` reserves at column 0.

### Changed

- Daemon git/gh calls at six sites — workspace `git fetch origin <base>`, `git push -u origin HEAD`, `gh pr create`, the post-create `gh api repos/.../pulls/{n}` author lookup, PR state polling for worktree cleanup, and PR comment listing — now retry transient failures up to five times with hardcoded exponential backoff (1s, 2s, 4s, 8s, 16s) and ±25% jitter before propagating the original error to the existing caller. PR comment POSTs (`vcs.PostPRComment`) remain one-shot because POST is non-idempotent and retrying could create duplicate comments. `gh pr create` is non-idempotent as well, so when its retry budget is exhausted Galley probes the current branch with `gh pr view` and, if a PR exists there, recovers its URL — covering the case where the first create succeeded server-side but the response was lost. The retry helper is internal-only and adds no new configurable surface: no new CLI flag, environment variable, task YAML field, profile YAML field, or executor/supervisor JSON schema is introduced. The recovery probe above is the only new shell invocation, and it is read-only.

## v0.3.4 - 2026-05-12

### Changed

- Accepted PR finalization now stages the accepted final diff instead of only `task.scope.allowed_paths`, so reviewable scope expansion approved by the supervisor is included in the PR. Changes inside `task.scope.forbidden_paths` still block finalization, and scope expansion is recorded in PR discussion items for reviewer attention.

## v0.3.3 - 2026-05-12

### Changed

- Completed PR worktree cleanup now removes the managed task worktree with `git worktree remove --force` and clears leftover non-Git directories, so ignored or generated files no longer leave repeated daemon cleanup errors after a PR is closed or merged.
- Supervisor prompts now classify blocking findings by the next actor: executor-actionable blockers request `needs_revision`, human judgment blockers request `needs_supervisor_review`, and external or unrecoverable blockers request `hard_stop`.

## v0.3.2 - 2026-05-12

### Changed

- Packaged Claude and Codex Galley plugins are now versioned as `0.1.5`: setup guidance treats setup as a first-time Galley experience, presenting available Galley-specific options and their meanings before asking users to approve profile authoring, daemon startup, PR automation, or queueing decisions.
- PR comment commands such as `/galley` now authorize solely by `comment.user.login == pr.author_login` and no longer read GitHub `author_association`. Comments from any other GitHub user are marked processed without requeueing or mutating revision requests, and task files without a recorded `pr.author_login` still fail closed.

## v0.3.1 - 2026-05-11

### Added

- Built-in model supervisor evaluations now recover from a stalled supervisor subprocess. When the supervisor exits because of idle timeout, total timeout, or a forced subprocess kill, Galley retries the same supervisor evaluation up to two additional times inside the same executor attempt before failing the task. The executor attempt is not retried — only the supervisor evaluation is re-run, so the existing executor diff and run evidence are preserved. Each try writes its own evidence under `runs/<run-id>/attempt-N/supervisor-try-<M>/` (`supervisor_error.json` with the classified `kind` for failed tries, `supervisor_verdict.json` on the successful try); the top-level `model_supervisor_verdict.json` is only written when a try succeeds. Exhausted retries record a supervisor-phase error with the classified kind on the attempt and move the task to `tasks/failed/` with status `needs_supervisor_review`. The retry budget is a fixed internal value; no new CLI flag, task YAML field, or profile field is introduced.

### Changed

- `galley daemon stop --force` now cleans up the daemon's known active executor and supervisor child process groups before reporting a stopped state. After the daemon process itself is gone, Galley SIGKILLs every registered child process group recorded under the daemon root, waits up to `--stop-timeout` for those groups to exit, and only then removes the PID file and prints the stopped message. If any registered child process group is still alive after the wait, the command returns a visible error that names the surviving PIDs and PGIDs and intentionally leaves the PID file in place so an operator can target the same daemon record instead of seeing a falsely-clean stop report. Tracking is bounded to subprocess process groups Galley itself started via `RunCommand`; generic OS PIDs are never added, and a record is pruned on read only when its registered process group is no longer alive — a record whose leader PID has already exited but whose process group still has surviving members stays tracked, so a force-killed or reaped leader cannot orphan descendants and those descendants remain targets of the force-stop cleanup. The leader PID is consulted only as a fallback for records that carry no process group (non-Unix platforms).
- Generated PR title rune budget is raised toward GitHub's 256-byte PR title limit so ordinary long ASCII task goals (for example PR 18's goal) are preserved verbatim instead of being prematurely truncated mid-sentence. The 256-byte PR title hard limit, whitespace-preferred word-boundary cut, valid UTF-8 boundary preservation, and trailing `…` marker on actual truncations are unchanged. Task YAML structure is unchanged: no new PR title field, schema property, profile setting, CLI flag, or skill prompt requirement is introduced.
- PR comment commands such as `/galley` are now restricted to the pull request author in addition to the existing GitHub `author_association` trust check. The new trust boundary is `(OWNER || COLLABORATOR) && comment.user.login == pr.author_login`. Galley persists the PR author login on the task YAML (`pr.author_login`) at PR creation time and preserves it across queue, requeue, and task load/save. Comments from another GitHub user — even when their `author_association` is `OWNER` or `COLLABORATOR` — are marked processed without requeueing or mutating revision requests, and when `pr.comments.reply` is enabled Galley posts a concise rejection reply that does not echo the user-supplied request body. Task files written before this change have no persisted PR author; Galley fails closed for those tasks and rejects PR commands until the author is recorded.
- Claude and Codex supervisor prompts now treat task input files as implementation source materials and check whether the final implementation preserves the requested core mechanism instead of substituting a weaker surrogate. The Claude executor prompt now classifies input materials before editing, maps quality profile dimensions into implementation rules, registers ordered Step gates with TodoWrite when available, and runs a self quality gate before returning the final executor JSON.

## v0.3.0 - 2026-05-11

### Added

- Idle-output watchdog for executor runs and built-in model supervisor runs. When a subprocess produces no stdout/stderr for `--idle-timeout` (default 10 minutes; configurable on the daemon), Galley terminates its process group and records a distinct idle-timeout result instead of hanging on a stalled command. The attempt is marked with `error_kind: idle_timeout` and `claude_status: idle_timed_out`, `runs/<run-id>/.../run_result.json` gains `idle_timed_out: true`, and the task loop continues according to its loop budget. The watchdog is independent of the existing total per-attempt timeout.
- `galley daemon stop --force`: after the graceful `SIGTERM` and `--stop-timeout`, the daemon's process identity is re-verified and `SIGKILL` is sent if it has not exited. The PID file is still removed only when it points at the stopped process.
- On startup the daemon now records the owning daemon for each claimed running task and immediately requeues running tasks whose recorded owner is dead or cannot be verified, without waiting for `--claim-ttl`. Running tasks still owned by a verified live daemon are preserved. A running task with no recorded owner — claimed by an older Galley, a claim recorded before its owner sidecar was written, or live work from a concurrently running daemon that did not record ownership — is left for the mtime-based `--claim-ttl` recovery, which continues to run each cycle as a backstop, so freshly claimed live work is never requeued.
- Optional `preflight.acceptance_skeleton` task YAML section. When `enabled: true`, the daemon runs a built-in test-creator stage after `inputfiles.Prepare` and before the first executor attempt, writes AC-linked test skeleton files, persists `runs/<run-id>/preflight_result.json` as the runtime source of truth, records generated `outputs[]` back into the running task, annotates AC verification with the skeleton path / satisfied behavior / integration point, and adds runtime skeleton obligations to the executor work order. Creator manifests are validated for AC IDs, safe relative paths, allowed/forbidden scope, duplicate paths, required metadata, and on-disk files. `galley task show` now lists runtime skeleton outputs in text output. The required-check acceptance gate treats a required quality check's `preferred_commands` as an ordered fallback list (mirroring `result.Complete`): a check is satisfied when any preferred command has passing evidence, failed when none passed but one has a failed entry, and missing only when no executor result recorded evidence for any of its commands.

### Changed

- Restart recovery can reuse an existing task worktree that still has context-only (`commit: false`) input files from a prior run: a destination whose content already matches the task input source is refreshed for a clean re-copy, while a destination with conflicting content fails the claimed task in the `input_files` phase with clear evidence instead of crashing the workspace setup. Committed input files keep the previous overwrite-refusing behavior. The reconciler resolves each destination through the same containment rules as input-file preparation before reading or removing it, so a symlinked destination parent that escapes the worktree is left for the normal input-file validation to reject rather than redirecting a read or delete outside the worktree.
- Packaged Claude and Codex Galley plugins are now versioned as `0.1.3`: task-authoring guidance improves reference-file intake order, treats supplied plans as single-task implementation guidance, scales execution settings from ordinary-task baselines, and favors referenceable decision items over fixed table layouts.
- Packaged Claude and Codex Galley plugins are now versioned as `0.1.4`: task authoring presents AC test skeleton preflight as one Task YAML execution setting and leaves generated test output details to the skeleton creator.

## v0.2.1 - 2026-05-10

### Changed

- Refined the Galley skill entrypoint into a flow catalog so task authoring, profile setup, daemon setup, queueing, troubleshooting, and Codex daemon use load the intended references.
- Profile setup now asks review strictness before repository inspection and keeps schema defaults, repository evidence, and user policy separate when drafting profiles.
- Task authoring now keeps reference-file intake consistent: supplied files are read after path/content, execution workspace destination, and commit policy are known.
- The OpenAI agent prompt now delegates to the Galley skill flow catalog and routed references instead of carrying a separate profile/task setup procedure.
- The packaged Claude and Codex Galley plugins are versioned as `0.1.2`.
- PR body acceptance criterion lines now reflect the supervisor verdict (`satisfied` / `partially_satisfied`) instead of the original draft `pending` placeholder. Acceptance criterion IDs listed under the supervisor's `acceptance_gaps` render as `partially_satisfied`; everything else under an accepted verdict reads as `satisfied`.
- `galley task show` no longer surfaces the last attempt's `latest_claude_status` and `latest_error_*` fields as if they were the active state once a task has reached an accepted terminal status (`accepted`, `pr_opened`, `closed`, or `merged`). The same fields are relabeled under the `prior_attempt_*` prefix so the audit trail remains visible after the daemon's PR cleanup loop transitions the task without regressing to active "failed" framing.
- Generated PR titles are now truncated at the last whitespace boundary inside the rune budget and append a single `…` ellipsis marker. The truncation also enforces GitHub's 256-byte PR title limit while preserving valid UTF-8 boundaries, so goals containing 4-byte runes (for example emoji) no longer overflow the limit. Goals without any whitespace inside the budget fall back to a hard rune-aligned cut and still receive the ellipsis.
- PR comment intake now accepts any comment whose trimmed body starts with `/galley` (including free-form requests like `/galley fix the failing test`); `/galley rerun` and `/galley requeue` remain backward-compatible aliases. Reply comments are concise acknowledgements that no longer quote the user-supplied request body, while the parsed request text is still preserved on the requeued task as a `RevisionRequest`.
- AFK task worktrees are now created from the environment profile's `pr.base` ref. When the source repository has an `origin` remote, Galley runs `git fetch --no-tags --quiet origin <pr.base>` and, on success, uses `refs/remotes/origin/<pr.base>` as the start-point; if the fetch fails the daemon refuses to use a possibly stale remote-tracking ref and fails the claimed task in the `workspace` phase so `galley task show` exposes the reason. Origin-less local repositories fall back to `refs/heads/<pr.base>`, and an empty `pr.base` preserves the previous source-HEAD behavior.
- Supervisor review prompts now treat pending revision requests as entry points for checking adjacent cases that share the same changed path, contract, persisted state, or external boundary, so rerun attempts are not accepted solely because the direct comment text was addressed.

### Added

- `runs/<run-id>/validation.json` records auditable evidence — `valid`, `task_id`, `schema_version`, and `generated_at` (UTC, RFC3339 nano) — alongside the existing `errors`, `warnings`, and `task` fields. The file path is unchanged and decoders that ignore unknown fields keep working.

## v0.2.0 - 2026-05-10

### Added

- Generated task, quality profile, and environment profile schema references from Go contracts with `galley schema generate` and `galley schema check`.
- Repository environment profiles now define PR creation, PR comment handling, base branch, and managed worktree cleanup defaults.
- `galley version` and `galley --version` report build version metadata.
- Task IDs generated by the skill skeleton include timestamp and random suffix components to avoid slug collisions.

### Changed

- Daemon startup options are focused on runtime operation; repository policy now comes from resolved profiles.
- `galley daemon run --once` drains queued tasks once, while background daemon runs also handle PR comments and cleanup maintenance.
- Profile setup in the Galley skill reads profile schemas first, proposes quality/environment settings from schema defaults plus repository evidence, and validates profiles after approval.
- Task authoring in the Galley skill uses the bundled task schema and skeleton script before validation and queueing.
- Queue duplicate-ID detection checks task IDs without depending on full validation of unrelated existing task files.
- The packaged Claude and Codex Galley plugins are versioned as `0.1.1`.

### Removed

- Repository manifest configuration and `examples/repos.yaml`.
- Daemon flags for repository profile files, manifest files, PR automation, PR comment handling, worktree cleanup, and prompt/schema override files.

## v0.1.0 - 2026-05-09

### Added

- Local Galley CLI with task validation, queueing, daemon execution, profile validation, and PR automation.
- Claude executor flow with structured evidence and supervisor review.
- Claude and Codex supervisor adapters with provider-specific review prompts.
- File-backed daemon queue with run evidence, stale claim handling, graceful shutdown, and conservative worktree cleanup.
- Agent Skill and plugin packaging for Claude Code and Codex.
- MIT license.
- GitHub Release workflow and GoReleaser configuration for prebuilt CLI archives.

### Changed

- `scripts/install.sh` now installs prebuilt GitHub Release assets by default, with `--local` and `--go-install` as explicit alternatives.
