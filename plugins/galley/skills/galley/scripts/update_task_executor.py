#!/usr/bin/env python3
"""Update executor overrides in an existing Galley task YAML file."""

from __future__ import annotations

import argparse
import json
import pathlib
import re
import sys
from typing import Final


FIELDS: Final = ("cli", "model", "effort")
UNCHANGED: Final = object()
EXECUTOR_HEADER = re.compile(r"^executor:[ \t]*(?:\{\})?[ \t]*(?:\r?\n|$)", re.MULTILINE)
ANY_EXECUTOR_HEADER = re.compile(r"^executor:", re.MULTILINE)
TOP_LEVEL_KEY = re.compile(r"^[A-Za-z0-9_-]+:", re.MULTILINE)
FIELD_LINE = re.compile(r"^  (cli|model|effort):[ \t]*(.*)$")
ANY_FIELD_LINE = re.compile(r"^\s+(cli|model|effort):")
INSERT_BEFORE = re.compile(
    r"^(?:preflight|decisions|risks|discussion_items|revision_requests|attempts|verification|pr):",
    re.MULTILINE,
)


class UpdateError(ValueError):
    """Raised when the canonical executor mapping cannot be updated."""


def executor_block(text: str) -> tuple[int, int, str] | None:
    headers = list(EXECUTOR_HEADER.finditer(text))
    if not headers:
        if ANY_EXECUTOR_HEADER.search(text):
            raise UpdateError("executor must use a block mapping before it can be updated")
        return None
    if len(headers) > 1:
        raise UpdateError("task contains multiple top-level executor mappings")

    header = headers[0]
    next_key = TOP_LEVEL_KEY.search(text, header.end())
    end = next_key.start() if next_key else len(text)
    return header.start(), end, text[header.end() : end]


def existing_fields(body: str) -> tuple[dict[str, str], list[str]]:
    values: dict[str, str] = {}
    extras: list[str] = []
    for line in body.splitlines():
        match = FIELD_LINE.fullmatch(line)
        if match:
            field = match.group(1)
            if field in values:
                raise UpdateError(f"executor.{field} appears more than once")
            values[field] = match.group(2)
        elif ANY_FIELD_LINE.match(line):
            raise UpdateError("executor fields must use two-space indentation")
        elif line.strip():
            extras.append(line)
    return values, extras


def rendered_block(values: dict[str, str], extras: list[str], newline: str) -> str:
    lines = [f"  {field}: {values[field]}" for field in FIELDS if field in values]
    lines.extend(extras)
    if not lines:
        return ""
    return newline.join(["executor:", *lines]) + newline


def update_executor_yaml(text: str, changes: dict[str, object]) -> str:
    newline = "\r\n" if "\r\n" in text else "\n"
    block = executor_block(text)
    values, extras = existing_fields(block[2]) if block else ({}, [])
    for field, requested in changes.items():
        if requested is UNCHANGED:
            continue
        if requested is None:
            values.pop(field, None)
        else:
            values[field] = json.dumps(requested, ensure_ascii=False)

    rendered = rendered_block(values, extras, newline)
    if block:
        return text[: block[0]] + rendered + text[block[1] :]
    if not rendered:
        return text
    insertion = INSERT_BEFORE.search(text)
    at = insertion.start() if insertion else len(text)
    return text[:at] + rendered + text[at:]


def parse_args() -> tuple[pathlib.Path, dict[str, object]]:
    parser = argparse.ArgumentParser(description="Update executor overrides in a Galley task YAML file.")
    parser.add_argument("task_file", type=pathlib.Path)
    for field in FIELDS:
        group = parser.add_mutually_exclusive_group()
        group.add_argument(f"--{field}", default=UNCHANGED)
        group.add_argument(f"--unset-{field}", dest=field, action="store_const", const=None)
    args = parser.parse_args()
    changes = {field: getattr(args, field) for field in FIELDS}
    if all(value is UNCHANGED for value in changes.values()):
        parser.error("specify at least one executor field to set or unset")
    return args.task_file, changes


def main() -> int:
    task_path, changes = parse_args()
    try:
        with task_path.open("r", encoding="utf-8", newline="") as task_file:
            original = task_file.read()
        updated = update_executor_yaml(original, changes)
        if updated != original:
            with task_path.open("w", encoding="utf-8", newline="") as task_file:
                task_file.write(updated)
    except (OSError, UpdateError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2

    state = "updated" if updated != original else "unchanged"
    print(f"{state}: {task_path}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
