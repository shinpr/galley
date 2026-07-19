# PR Automation

Galley can turn an accepted task into a GitHub pull request and use trusted PR comments as the next review loop.

PR automation requires GitHub CLI (`gh`) to be installed and authenticated.

## Accepted Task Handoff

When `pr.enabled: true` is set in the environment profile, accepted worktree changes are committed with a Galley task commit message and `git_commit_result.json` is written to the run directory.

With `pr.enabled: true`, Galley pushes the current worktree branch to `origin`, writes `pr_body.md`, runs `gh pr create`, updates `pr.url` and `pr.status`, and moves the task to `done/pr_opened`.

For AFK implementation tasks, PR creation plus comment polling is the recommended review loop. Local-only runs can leave PR automation disabled when GitHub credentials or network access are unavailable.

The branch is the task YAML `worktree.branch`; the base branch comes from `pr.base`.

## Base Branch Resolution

Galley branches each new AFK task worktree from `pr.base`.

When an `origin` remote is configured, the daemon refreshes the remote-tracking ref with:

```sh
git fetch --no-tags --quiet origin <pr.base>
```

On success it uses `refs/remotes/origin/<pr.base>` as the start point. If that fetch fails, the daemon refuses to fall back to a possibly stale local `origin/<pr.base>` and fails the claimed task in the `workspace` phase so `galley task show` exposes the reason.

Origin-less local checkouts fall back to `refs/heads/<pr.base>`. The start point matches the eventual `gh pr create --base <pr.base>` target instead of inheriting whatever commit the source repository's HEAD currently points at.

## PR Body and Title

Galley renders each acceptance criterion in `pr_body.md` from the supervisor's current pass set. IDs in `acceptance_passes` read as `Status: satisfied`; other IDs read as `Status: partially_satisfied` so incomplete review state remains visible instead of blocking PR creation.

Generated PR titles preserve the task goal up to GitHub's title limit. When truncation is required, Galley cuts on a valid UTF-8 boundary and appends `…`.

## PR Comment Requeueing

With `pr.comments.enabled: true`, the daemon scans task files with `pr.url`, reads GitHub issue comments through `gh api`, and processes the oldest unprocessed Galley command for each task.

A comment is accepted when its body, after trimming surrounding whitespace, starts with `/galley`. Recognized forms are:

- `/galley <free-form request>`: the text after the prefix becomes the request reason, for example `/galley fix the failing test`.
- `/galley` alone: a no-arg requeue using the default reason.

Mid-line mentions like `Looks good, /galley`, a `/galley` line that appears only after the first non-whitespace line, `/galley:galley ...`, and `/galleyfoo ...` are ignored.

Processed comment IDs are stored in `pr.processed_comment_ids` so commands are not applied twice.

## Comment Replies

With `pr.comments.reply: true`, Galley posts a concise acknowledgement comment after handling a Galley command. The parsed request is preserved on the requeued task for the next executor attempt.

Reply forms:

- Successful requeue: `Galley requeued task <task-id> from this comment.`
- Comment received while the task is queued or running: `Galley noted this comment; task is already <status>.`
- Comment from a non-PR author: `Galley ignored this comment because only the pull request author can run Galley from PR comments.`

## Worktree Cleanup

With `worktree.cleanup: true`, the daemon scans `tasks/done` PR tasks and checks PR state through `gh api`.

- Open PRs keep their worktree.
- Closed or merged PRs remove the managed task worktree, including uncommitted or generated files left in that worktree.

Cleanup refuses to remove the source repository itself or one of its ancestor directories.
