#!/usr/bin/env python3
"""Validate and queue a Galley task after user approval."""

from __future__ import annotations

import argparse
import shutil
import subprocess
import sys


def run(argv: list[str]) -> int:
    result = subprocess.run(argv, text=True, check=False)
    return result.returncode


def main() -> int:
    parser = argparse.ArgumentParser(description="Validate and queue a Galley task file.")
    parser.add_argument("task_file")
    parser.add_argument("--galley", default="galley", help="Galley CLI path.")
    parser.add_argument("--skip-validate", action="store_true")
    parser.add_argument("--reason", help="Reason to record when queueing.")
    parser.add_argument("--root", help="Explicit Galley daemon root for advanced multi-root workflows.")
    parser.add_argument("--move", action="store_true", help="Remove the source task file after queueing.")
    args = parser.parse_args()

    galley = shutil.which(args.galley) or args.galley
    if shutil.which(args.galley) is None and galley == args.galley:
        print(f"Galley CLI not found: {args.galley}", file=sys.stderr)
        return 127

    if not args.skip_validate:
        code = run([galley, "task", "validate", args.task_file])
        if code != 0:
            return code
    queue_cmd = [galley, "task", "queue", args.task_file]
    if args.reason:
        queue_cmd.extend(["--reason", args.reason])
    if args.root:
        queue_cmd.extend(["--root", args.root])
    if args.move:
        queue_cmd.append("--move")
    return run(queue_cmd)


if __name__ == "__main__":
    raise SystemExit(main())
