#!/usr/bin/env python3
"""Run galley task validate with clearer missing-binary errors."""

from __future__ import annotations

import argparse
import shutil
import subprocess
import sys


def main() -> int:
    parser = argparse.ArgumentParser(description="Validate a Galley task file.")
    parser.add_argument("task_file")
    parser.add_argument("--galley", default="galley", help="Galley CLI path.")
    args = parser.parse_args()

    galley = shutil.which(args.galley) or args.galley
    try:
        result = subprocess.run(
            [galley, "task", "validate", args.task_file],
            text=True,
            check=False,
        )
    except FileNotFoundError:
        print(f"Galley CLI not found: {args.galley}", file=sys.stderr)
        return 127
    return result.returncode


if __name__ == "__main__":
    raise SystemExit(main())
