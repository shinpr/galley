#!/usr/bin/env python3
"""Create a Galley task YAML skeleton in draft status."""

from __future__ import annotations

import argparse
import datetime as dt
import hashlib
import json
import os
import pathlib
import re
import secrets
import shutil
import subprocess
import sys
from typing import Any


SCRIPT_DIR = pathlib.Path(__file__).resolve().parent
SCHEMA_PATH = SCRIPT_DIR.parent / "references" / "task.schema.json"
PROFILE_LOADER_DIR = SCRIPT_DIR / "profile_loader"
VALID_EXECUTOR_CLIS = {"claude", "codex", "glm", "grok"}
ROOT_ORDER = [
    "id",
    "mode",
    "status",
    "goal",
    "acceptance_criteria",
    "scope",
    "files",
    "execution_policy",
    "worktree",
    "supervisor",
    "executor",
    "preflight",
    "decisions",
    "risks",
    "discussion_items",
    "revision_requests",
    "attempts",
    "verification",
    "pr",
]


def slugify(value: str) -> str:
    slug = re.sub(r"[^A-Za-z0-9._-]+", "-", value.strip().lower())
    slug = re.sub(r"-+", "-", slug).strip("-")
    return slug or "task"


def task_id_for(title: str) -> str:
    timestamp = dt.datetime.now(dt.UTC).strftime("%Y%m%d%H%M%S")
    random_suffix = secrets.token_hex(3)
    return f"task-{timestamp}-{random_suffix}-{slugify(title)}"


def repo_key_for(cwd: pathlib.Path) -> str:
    absolute = cwd.expanduser().resolve(strict=False)
    try:
        absolute = absolute.resolve(strict=True)
    except FileNotFoundError:
        pass
    base = re.sub(r"[^A-Za-z0-9._-]+", "-", absolute.name).strip("-._") or "repo"
    digest = hashlib.sha256(str(absolute).encode("utf-8")).hexdigest()[:8]
    return f"{base}-{digest}"


def default_galley_root() -> pathlib.Path:
    return pathlib.Path(os.environ.get("GALLEY_ROOT", pathlib.Path.home() / ".galley")).expanduser()


def environment_profile_path(root: pathlib.Path, cwd: pathlib.Path) -> pathlib.Path:
    return root / "profiles" / repo_key_for(cwd) / "environment.yaml"


def repository_root() -> pathlib.Path | None:
    for candidate in [SCRIPT_DIR, *SCRIPT_DIR.parents]:
        go_mod = candidate / "go.mod"
        # This repo-local helper is only authoritative inside the upstream
        # Galley module; forks or module renames should fall back to the
        # installed CLI or the small Python parser below.
        if go_mod.is_file() and 'module github.com/shinpr/galley' in go_mod.read_text(encoding="utf-8"):
            return candidate
    return None


def parse_executor_default_payload(stdout: str, path: pathlib.Path) -> str | None:
    try:
        payload = json.loads(stdout)
    except json.JSONDecodeError as exc:
        raise ValueError(f"profile loader returned invalid JSON: {exc}") from exc
    parsed = str(payload.get("default_cli") or "").strip()
    if parsed and parsed not in VALID_EXECUTOR_CLIS:
        raise ValueError(f"{path}: executor.default_cli must be one of: claude, codex, glm, grok")
    return parsed or None


def galley_binary() -> str | None:
    configured = os.environ.get("GALLEY_BIN", "").strip()
    if configured:
        return configured
    return shutil.which("galley")


def executor_default_from_profile_command(path: pathlib.Path) -> str | None:
    binary = galley_binary()
    if not binary:
        return None
    absolute_path = path.expanduser().resolve(strict=False)
    try:
        proc = subprocess.run(
            [binary, "profile", "executor-default", "--output", "json", str(absolute_path)],
            capture_output=True,
            text=True,
        )
    except FileNotFoundError:
        return None
    if proc.returncode != 0:
        message = proc.stderr.strip() or proc.stdout.strip() or "profile executor-default failed"
        if "unknown command" in message:
            return None
        if "executor.default_cli" in message:
            raise ValueError(message)
        return None
    return parse_executor_default_payload(proc.stdout, path)


def executor_default_from_profile_loader(path: pathlib.Path) -> str | None:
    if shutil.which("go") is None or not PROFILE_LOADER_DIR.is_dir():
        return None
    root = repository_root()
    if root is None:
        return None
    absolute_path = path.expanduser().resolve(strict=False)
    proc = subprocess.run(
        ["go", "run", "./plugins/galley/skills/galley/scripts/profile_loader", str(absolute_path)],
        cwd=root,
        capture_output=True,
        text=True,
    )
    if proc.returncode != 0:
        message = proc.stderr.strip() or proc.stdout.strip() or "profile loader failed"
        if "executor.default_cli" in message:
            raise ValueError(message)
        return None
    return parse_executor_default_payload(proc.stdout, path)


def strip_yaml_comment(value: str) -> str:
    quote: str | None = None
    escaped = False
    for i, char in enumerate(value):
        if quote == '"':
            if escaped:
                escaped = False
            elif char == "\\":
                escaped = True
            elif char == quote:
                quote = None
            continue
        if quote == "'":
            if char == quote:
                if i + 1 < len(value) and value[i + 1] == quote:
                    continue
                quote = None
            continue
        if char in {"'", '"'}:
            quote = char
        elif char == "#" and (i == 0 or value[i - 1].isspace()):
            return value[:i].rstrip()
    return value.strip()


def unquote_yaml_scalar(value: str) -> str:
    value = strip_yaml_comment(value).strip()
    if not value:
        return ""
    if value[0] in {"'", '"'} and value[-1:] == value[0]:
        try:
            return json.loads(value) if value[0] == '"' else value[1:-1].replace("''", "'")
        except json.JSONDecodeError:
            return value[1:-1]
    return value.strip()


def split_yaml_key_value(value: str) -> tuple[str, str, str]:
    quote: str | None = None
    escaped = False
    for i, char in enumerate(value):
        if quote == '"':
            if escaped:
                escaped = False
            elif char == "\\":
                escaped = True
            elif char == quote:
                quote = None
            continue
        if quote == "'":
            if char == quote:
                if i + 1 < len(value) and value[i + 1] == quote:
                    continue
                quote = None
            continue
        if char in {"'", '"'}:
            quote = char
        elif char == ":":
            return value[:i].strip(), ":", value[i + 1 :].strip()
    return value, "", ""


def parse_executor_flow_mapping(value: str, path: pathlib.Path) -> str | None:
    value = strip_yaml_comment(value)
    anchor_match = re.match(r"^&[A-Za-z0-9_.-]+\s+(.*)$", value)
    if anchor_match:
        value = anchor_match.group(1).strip()
    if value in {"", "{}", "null", "~"}:
        return None
    if not (value.startswith("{") and value.endswith("}")):
        return None
    body = value[1:-1].strip()
    if not body:
        return None
    parts: list[str] = []
    start = 0
    quote: str | None = None
    escaped = False
    for i, char in enumerate(body):
        if quote == '"':
            if escaped:
                escaped = False
            elif char == "\\":
                escaped = True
            elif char == quote:
                quote = None
            continue
        if quote == "'":
            if char == quote:
                if i + 1 < len(body) and body[i + 1] == quote:
                    continue
                quote = None
            continue
        if char in {"'", '"'}:
            quote = char
        elif char == ",":
            parts.append(body[start:i].strip())
            start = i + 1
    parts.append(body[start:].strip())
    for part in parts:
        key, sep, raw_value = split_yaml_key_value(part)
        if sep and unquote_yaml_scalar(key) == "default_cli":
            parsed = unquote_yaml_scalar(raw_value)
            if parsed in VALID_EXECUTOR_CLIS:
                return parsed
            raise ValueError(f"{path}: executor.default_cli must be one of: claude, codex, glm, grok")
    return None


def executor_default_from_environment(path: pathlib.Path) -> str | None:
    try:
        text = path.read_text(encoding="utf-8")
    except FileNotFoundError:
        return None

    for loader in (executor_default_from_profile_command, executor_default_from_profile_loader):
        try:
            loaded = loader(path)
        except ValueError as exc:
            if "executor.default_cli" in str(exc):
                raise
            loaded = None
        if loaded:
            return loaded

    in_executor = False
    executor_indent = 0
    for raw in text.splitlines():
        if not raw.strip() or raw.lstrip().startswith("#"):
            continue
        indent = len(raw) - len(raw.lstrip(" "))
        stripped = raw.strip()
        if not raw.startswith(" "):
            key, sep, value = split_yaml_key_value(stripped)
            if sep and unquote_yaml_scalar(key) == "executor":
                flow_value = parse_executor_flow_mapping(value, path)
                if flow_value:
                    return flow_value
                in_executor = strip_yaml_comment(value) == ""
                executor_indent = indent
                continue
        if in_executor and indent <= executor_indent:
            in_executor = False
        if in_executor:
            key, sep, value = split_yaml_key_value(stripped)
            if sep and unquote_yaml_scalar(key) == "default_cli":
                parsed = unquote_yaml_scalar(value)
                if parsed in VALID_EXECUTOR_CLIS:
                    return parsed
                raise ValueError(f"{path}: executor.default_cli must be one of: claude, codex, glm, grok")
    return None


def resolved_executor_cli(explicit_cli: str | None, root: pathlib.Path, cwd: pathlib.Path) -> str:
    if explicit_cli:
        return explicit_cli
    return executor_default_from_environment(environment_profile_path(root, cwd)) or "claude"


def executor_defaults(cli: str) -> dict[str, Any]:
    # Prompt transport is Galley-owned and is not authored into task YAML.
    # cli/model/effort remain independently optional at runtime. The skeleton
    # pins only cli for authoring convenience; model and effort are omitted so
    # the current repository environment.yaml (then built-in defaults) apply at
    # run time unless the author later pins them explicitly.
    return {
        "cli": cli if cli in VALID_EXECUTOR_CLIS else "claude",
    }


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
        help="Implementation executor backend for this task. Defaults to environment.executor.default_cli, then claude.",
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
    task["mode"] = "afk"
    task["status"] = "draft"
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
        "afk_decision_policy": "choose-smallest-reversible",
        "stop_on_destructive_operation": True,
        "stop_on_missing_secret": True,
        "stop_on_external_service_unavailable": True,
    }
    task["worktree"] = {
        "enabled": True,
        "branch": f"agent/{task_id}",
        "path": f"../{cwd.name}.worktrees/{task_id}",
    }
    task["supervisor"] = {"review_iterations": 0}
    try:
        task["executor"] = executor_defaults(resolved_executor_cli(args.executor_cli, root, cwd))
    except ValueError as exc:
        print(str(exc), file=sys.stderr)
        return 2
    # Emit the AC test skeleton preflight stage explicitly disabled by default so
    # the generated YAML shows the runtime gate. Only `enabled` is written; any
    # enabled-only fields (mode, required, allowed_paths, outputs) stay omitted
    # until the author opts in by flipping enabled to true.
    task["preflight"] = {"acceptance_skeleton": {"enabled": False}}
    task["decisions"] = []
    task["risks"] = []
    task["attempts"] = []
    task["verification"] = {"commands": []}
    task["pr"] = {"url": "", "status": "", "processed_comment_ids": []}

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
