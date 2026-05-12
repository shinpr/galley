# Changelog

All notable changes to Galley are documented here.

This project follows semantic versioning.

## Unreleased

### Changed

- Completed PR worktree cleanup now removes the managed task worktree with `git worktree remove --force` and clears leftover non-Git directories, so ignored or generated files no longer leave repeated daemon cleanup errors after a PR is closed or merged.

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
