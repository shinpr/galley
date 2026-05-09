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
    args = parser.parse_args()

    galley = shutil.which(args.galley) or args.galley
    if shutil.which(args.galley) is None and galley == args.galley:
        print(f"Galley CLI not found: {args.galley}", file=sys.stderr)
        return 127

    if not args.skip_validate:
        code = run([galley, "task", "validate", args.task_file])
        if code != 0:
            return code
    return run([galley, "task", "queue", args.task_file])


if __name__ == "__main__":
    raise SystemExit(main())
