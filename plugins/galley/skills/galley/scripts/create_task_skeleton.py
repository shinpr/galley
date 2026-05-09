#!/usr/bin/env python3
"""Create a Galley task YAML skeleton in draft status."""

from __future__ import annotations

import argparse
import datetime as dt
import pathlib
import re
import sys


def slugify(value: str) -> str:
    slug = re.sub(r"[^A-Za-z0-9._-]+", "-", value.strip().lower())
    slug = re.sub(r"-+", "-", slug).strip("-")
    return slug or "task"


def parse_file_mapping(value: str) -> tuple[str, str]:
    source, sep, destination = value.partition("=")
    if not sep or not source.strip() or not destination.strip():
        raise argparse.ArgumentTypeError("expected SOURCE=DESTINATION")
    return source.strip(), destination.strip()


def main() -> int:
    parser = argparse.ArgumentParser(description="Create a Galley task YAML skeleton.")
    parser.add_argument("title", help="Short task title used for id and branch.")
    parser.add_argument("--cwd", required=True, help="Absolute target repository path.")
    parser.add_argument("--root", default=str(pathlib.Path.home() / ".galley"), help="Galley daemon root.")
    parser.add_argument("--loop-budget", default="2", help='Positive integer or "infinite".')
    parser.add_argument("--allowed-path", action="append", default=["."], help="Relative path allowed for edits. Repeatable.")
    parser.add_argument(
        "--permission",
        default="sandbox-full-access",
        choices=["read-only", "edit", "sandbox-full-access"],
        help="Task permission level.",
    )
    parser.add_argument(
        "--reference-file",
        "--input-file",
        dest="reference_file",
        action="append",
        default=[],
        type=parse_file_mapping,
        metavar="SOURCE=DESTINATION",
        help="Copy a context-only spec, work plan, log, screenshot note, or other reference file into the worktree. Repeatable. The file is removed before commit/PR.",
    )
    parser.add_argument(
        "--committed-file",
        action="append",
        default=[],
        type=parse_file_mapping,
        metavar="SOURCE=DESTINATION",
        help="Copy a supplied file into the worktree and allow it to remain in the final diff. Repeatable.",
    )
    args = parser.parse_args()

    cwd = pathlib.Path(args.cwd).expanduser()
    if not cwd.is_absolute():
        print("--cwd must be an absolute path", file=sys.stderr)
        return 2

    short = slugify(args.title)
    task_id = f"task-{dt.datetime.now(dt.UTC).strftime('%Y%m%d')}-{short}"
    root = pathlib.Path(args.root)
    draft_dir = root / "tasks" / "draft"
    draft_dir.mkdir(parents=True, exist_ok=True)
    task_file = draft_dir / f"{task_id}.yaml"

    allowed = "\n".join(f"    - {path}" for path in args.allowed_path)
    file_entries: list[str] = []
    for source, destination in args.reference_file:
        file_entries.append(
            "\n".join(
                [
                    f"  - source: {source}",
                    f"    destination: {destination}",
                    "    description: TODO: explain which spec, work plan, log, or reference this provides.",
                    "    commit: false",
                ]
            )
        )
    for source, destination in args.committed_file:
        file_entries.append(
            "\n".join(
                [
                    f"  - source: {source}",
                    f"    destination: {destination}",
                    "    description: TODO: explain why this file should be committed.",
                    "    commit: true",
                ]
            )
        )
    files_block = ""
    if file_entries:
        files_block = "files:\n" + "\n".join(file_entries) + "\n"
    content = f"""id: {task_id}
mode: afk
status: draft
goal: TODO: replace with one concrete outcome.
acceptance_criteria:
  - id: AC1
    text: TODO: observable requirement.
    verification: TODO: command or evidence source.
    status: pending
scope:
  cwd: {cwd}
  allowed_paths:
{allowed}
  forbidden_paths:
    - .env
    - .env.local
    - .git
  permission: {args.permission}
{files_block}execution_policy:
  loop_budget: {args.loop_budget}
  timeout_ms: 1800000
  afk_decision_policy: choose-smallest-reversible
  stop_on_destructive_operation: true
  stop_on_missing_secret: true
  stop_on_external_service_unavailable: true
worktree:
  enabled: true
  branch: agent/{short}
  path: ../{cwd.name}.worktrees/{short}
supervisor:
  review_iterations: 1
executor:
  cli: claude
  model: opus
  effort: high
  prompt_profile: codexized-claude-executor-v1
  prompt_mode: replace
  max_budget_usd: 4
decisions: []
risks: []
attempts: []
verification:
  commands: []
pr:
  url: ""
  status: ""
  processed_comment_ids: []
"""
    task_file.write_text(content, encoding="utf-8")
    print(task_file)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
