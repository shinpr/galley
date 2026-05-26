#!/usr/bin/env python3
"""Print a compact index of useful Galley run evidence files."""

from __future__ import annotations

import argparse
import json
import pathlib


INTERESTING = {
    "work_order.md",
    "diff.patch",
    "command_plan.json",
    "supervisor_verdict.json",
    "codex_supervisor_request.json",
    "claude_supervisor_request.json",
    "model_supervisor_verdict.json",
    "executor_result.json",
}


def summarize_json(path: pathlib.Path) -> str:
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
    except Exception as exc:  # noqa: BLE001 - diagnostic script should keep going.
        return f"unreadable json: {exc}"
    if isinstance(data, dict):
        parts = []
        for key in ("status", "summary", "verdict", "confidence"):
            if key in data:
                parts.append(f"{key}={data[key]!r}")
        return ", ".join(parts) if parts else f"keys={','.join(sorted(data.keys())[:8])}"
    return type(data).__name__


def main() -> int:
    parser = argparse.ArgumentParser(description="List useful evidence in a Galley run directory.")
    parser.add_argument("run_dir")
    args = parser.parse_args()

    root = pathlib.Path(args.run_dir)
    if not root.exists():
        print(f"missing run directory: {root}")
        return 2

    for path in sorted(root.rglob("*")):
        if not path.is_file() or path.name not in INTERESTING:
            continue
        rel = path.relative_to(root)
        detail = ""
        if path.suffix == ".json":
            detail = f" - {summarize_json(path)}"
        print(f"{rel}{detail}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
