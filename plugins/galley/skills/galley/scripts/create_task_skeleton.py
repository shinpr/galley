#!/usr/bin/env python3
"""Create a Galley task YAML skeleton in draft status."""

from __future__ import annotations

import argparse
import datetime as dt
import json
import os
import pathlib
import re
import secrets
import sys
from typing import Any


SCRIPT_DIR = pathlib.Path(__file__).resolve().parent
SCHEMA_PATH = SCRIPT_DIR.parent / "references" / "task.schema.json"
VALID_EXECUTOR_CLIS = {"claude", "codex", "glm", "grok"}
ROOT_ORDER = [
    "id",
    "goal",
    "acceptance_criteria",
    "scope",
    "files",
    "execution_policy",
    "worktree",
    "executor",
    "preflight",
    "decisions",
    "risks",
    "discussion_items",
    "revision_requests",
]


def slugify(value: str) -> str:
    slug = re.sub(r"[^A-Za-z0-9._-]+", "-", value.strip().lower())
    slug = re.sub(r"-+", "-", slug).strip("-")
    return slug or "task"


def task_id_for(title: str) -> str:
    timestamp = dt.datetime.now(dt.UTC).strftime("%Y%m%d%H%M%S")
    random_suffix = secrets.token_hex(3)
    return f"task-{timestamp}-{random_suffix}-{slugify(title)}"


def default_galley_root() -> pathlib.Path:
    return pathlib.Path(os.environ.get("GALLEY_ROOT", pathlib.Path.home() / ".galley")).expanduser()


def parse_file_mapping(value: str) -> tuple[str, str]:
    source, sep, destination = value.partition("=")
    if not sep or not source.strip() or not destination.strip():
        raise argparse.ArgumentTypeError("expected SOURCE=DESTINATION")
    return source.strip(), destination.strip()


def parse_nonnegative_int(value: str) -> int:
    try:
        parsed = int(value)
    except ValueError as exc:
        raise argparse.ArgumentTypeError("expected an integer >= 0") from exc
    if parsed < 0:
        raise argparse.ArgumentTypeError("expected an integer >= 0")
    return parsed


def load_task_schema() -> dict[str, Any]:
    try:
        return json.loads(SCHEMA_PATH.read_text(encoding="utf-8"))
    except FileNotFoundError:
        print(f"task schema not found: {SCHEMA_PATH}", file=sys.stderr)
        raise SystemExit(2)
    except json.JSONDecodeError as exc:
        print(f"invalid task schema JSON: {exc}", file=sys.stderr)
        raise SystemExit(2)


def skeleton_from_schema(schema: dict[str, Any]) -> Any:
    schema_type = schema.get("type")
    if "enum" in schema:
        values = schema.get("enum") or []
        return values[0] if values else ""
    if schema_type == "object":
        props = schema.get("properties", {})
        required = schema.get("required", [])
        return {name: skeleton_from_schema(props[name]) for name in required if name in props}
    if schema_type == "array":
        if schema.get("minItems", 0) > 0:
            return [skeleton_from_schema(schema.get("items", {}))]
        return []
    if schema_type == "integer":
        return int(schema.get("minimum", 0))
    if schema_type == "number":
        return float(schema.get("minimum", 0))
    if schema_type == "boolean":
        return False
    return ""


def ordered_items(mapping: dict[str, Any], preferred: list[str]) -> list[tuple[str, Any]]:
    seen = set()
    items: list[tuple[str, Any]] = []
    for key in preferred:
        if key in mapping:
            items.append((key, mapping[key]))
            seen.add(key)
    for key, value in mapping.items():
        if key not in seen:
            items.append((key, value))
    return items


def yaml_scalar(value: Any) -> str:
    if isinstance(value, bool):
        return "true" if value else "false"
    if isinstance(value, (int, float)):
        return str(value)
    if value == "":
        return '""'
    text = str(value)
    if re.fullmatch(r"[A-Za-z0-9._/\-]+", text):
        return text
    return json.dumps(text, ensure_ascii=False)


def dump_yaml(value: Any, indent: int = 0, key_order: list[str] | None = None) -> str:
    pad = " " * indent
    if isinstance(value, dict):
        lines: list[str] = []
        preferred = key_order or []
        for key, child in ordered_items(value, preferred):
            if isinstance(child, (dict, list)) and child:
                lines.append(f"{pad}{key}:")
                lines.append(dump_yaml(child, indent + 2))
            elif isinstance(child, list):
                lines.append(f"{pad}{key}: []")
            elif isinstance(child, dict):
                lines.append(f"{pad}{key}: {{}}")
            else:
                lines.append(f"{pad}{key}: {yaml_scalar(child)}")
        return "\n".join(lines)
    if isinstance(value, list):
        if not value:
            return f"{pad}[]"
        lines = []
        for item in value:
            if isinstance(item, dict):
                item_lines = dump_yaml(item, indent + 2).splitlines()
                lines.append(f"{pad}- {item_lines[0].lstrip()}")
                lines.extend(item_lines[1:])
            elif isinstance(item, list):
                lines.append(f"{pad}-")
                lines.append(dump_yaml(item, indent + 2))
            else:
                lines.append(f"{pad}- {yaml_scalar(item)}")
        return "\n".join(lines)
    return f"{pad}{yaml_scalar(value)}"


def main() -> int:
    parser = argparse.ArgumentParser(description="Create a Galley task YAML skeleton.")
    parser.add_argument("title", help="Short task title used for id and branch.")
    parser.add_argument("--cwd", required=True, help="Absolute target repository path.")
    parser.add_argument("--output-dir", default=".", help="Directory for the draft task YAML.")
    parser.add_argument("--root", help=argparse.SUPPRESS)
    parser.add_argument(
        "--executor-cli",
        choices=sorted(VALID_EXECUTOR_CLIS),
        help="Pin the implementation executor backend in this task. Omit to use runtime environment defaults.",
    )
    parser.add_argument("--loop-budget", default=10, type=parse_nonnegative_int, help="Integer >= 0; 0 means unlimited.")
    parser.add_argument("--allowed-path", action="append", default=None, help="Relative path allowed for edits. Repeatable.")
    parser.add_argument(
        "--permission",
        default="sandbox-full-access",
        choices=["read-only", "edit", "sandbox-full-access"],
        help="Task authority level: read-only, edit, or broad operations inside an isolated worktree.",
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

    task_id = task_id_for(args.title)
    root = pathlib.Path(args.root).expanduser() if args.root else default_galley_root()
    if args.root:
        output_dir = root / "tasks" / "draft"
    else:
        output_dir = pathlib.Path(args.output_dir).expanduser()
    output_dir.mkdir(parents=True, exist_ok=True)
    task_file = output_dir / f"{task_id}.yaml"

    schema = load_task_schema()
    task = skeleton_from_schema(schema)
    if not isinstance(task, dict):
        print("task schema root must be an object", file=sys.stderr)
        return 2

    task["id"] = task_id
    # mode, status, worktree.enabled, and AFK decision policy are fixed runtime
    # defaults applied by Galley when omitted. Daemon-owned lifecycle sections
    # (supervisor, attempts, verification, pr) are omitted from author drafts.
    task.pop("mode", None)
    task.pop("status", None)
    task.pop("supervisor", None)
    task.pop("attempts", None)
    task.pop("verification", None)
    task.pop("pr", None)
    task["goal"] = "TODO: replace with one concrete outcome."
    task["acceptance_criteria"] = [
        {
            "id": "AC1",
            "text": "TODO: observable requirement.",
            "verification": "TODO: verification method or evidence source.",
            "status": "pending",
        }
    ]
    task["scope"] = {
        "cwd": str(cwd),
        "allowed_paths": args.allowed_path or ["."],
        "forbidden_paths": [".env", ".env.local", ".git"],
        "permission": args.permission,
    }
    task["execution_policy"] = {
        "loop_budget": args.loop_budget,
        "timeout_ms": 1800000,
    }
    task["worktree"] = {
        "branch": f"agent/{task_id}",
        "path": f"../{cwd.name}.worktrees/{task_id}",
    }
    if args.executor_cli:
        task["executor"] = {"cli": args.executor_cli}
    else:
        task.pop("executor", None)
    # Emit the AC test skeleton preflight stage explicitly disabled by default so
    # the generated YAML shows the author opt-in. Only `enabled` is authored;
    # outputs are daemon-owned runtime metadata.
    task["preflight"] = {"acceptance_skeleton": {"enabled": False}}
    task["decisions"] = []
    task["risks"] = []

    file_entries: list[dict[str, Any]] = []
    for source, destination in args.reference_file:
        file_entries.append(
            {
                "source": source,
                "destination": destination,
                "description": "TODO: explain which spec, work plan, log, or reference this provides.",
                "commit": False,
            }
        )
    for source, destination in args.committed_file:
        file_entries.append(
            {
                "source": source,
                "destination": destination,
                "description": "TODO: explain why this file should be committed.",
                "commit": True,
            }
        )
    if file_entries:
        task["files"] = file_entries
    elif "files" in task:
        del task["files"]

    content = dump_yaml(task, key_order=ROOT_ORDER) + "\n"
    task_file.write_text(content, encoding="utf-8")
    print(task_file)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
