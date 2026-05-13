#!/usr/bin/env python3
"""Focused regression checks for create_task_skeleton.py.

These checks fail if the generated skeleton YAML stops emitting the default
disabled acceptance skeleton preflight gate, or if it accidentally starts
emitting opt-in-only runtime fields such as `outputs` in the default skeleton.

Run directly:

    python3 plugins/galley/skills/galley/scripts/test_create_task_skeleton.py

Exit status 0 means the contract holds; any non-zero exit means the contract
regressed and the message names the missing or unexpected field.
"""

from __future__ import annotations

import pathlib
import re
import subprocess
import sys
import tempfile


SCRIPT_DIR = pathlib.Path(__file__).resolve().parent
GENERATOR = SCRIPT_DIR / "create_task_skeleton.py"


def generate_skeleton(tmpdir: pathlib.Path) -> str:
    output_dir = tmpdir / "draft"
    output_dir.mkdir(parents=True, exist_ok=True)
    fake_cwd = tmpdir / "repo"
    fake_cwd.mkdir(parents=True, exist_ok=True)
    proc = subprocess.run(
        [
            sys.executable,
            str(GENERATOR),
            "demo-skeleton-preflight",
            "--cwd",
            str(fake_cwd),
            "--output-dir",
            str(output_dir),
        ],
        check=True,
        capture_output=True,
        text=True,
    )
    task_path = pathlib.Path(proc.stdout.strip())
    return task_path.read_text(encoding="utf-8")


def assert_contains(yaml_text: str, needle: str, description: str) -> None:
    if needle not in yaml_text:
        raise SystemExit(
            f"regression: generated skeleton is missing {description!r} "
            f"(expected substring: {needle!r})"
        )


def assert_not_matches(yaml_text: str, pattern: str, description: str) -> None:
    if re.search(pattern, yaml_text, re.MULTILINE):
        raise SystemExit(
            f"regression: generated skeleton unexpectedly emits {description!r} "
            f"(pattern: {pattern!r})"
        )


def main() -> int:
    with tempfile.TemporaryDirectory() as tmp:
        yaml_text = generate_skeleton(pathlib.Path(tmp))

    assert_contains(yaml_text, "preflight:\n", "preflight block header")
    assert_contains(
        yaml_text,
        "  acceptance_skeleton:\n    enabled: false\n",
        "preflight.acceptance_skeleton.enabled: false",
    )

    # Default skeleton must not include enabled-only runtime fields.
    assert_not_matches(yaml_text, r"^    outputs:", "outputs[] in default skeleton")
    assert_not_matches(yaml_text, r"^    required:", "required in default skeleton")
    assert_not_matches(yaml_text, r"^    allowed_paths:", "allowed_paths in default skeleton")
    assert_not_matches(yaml_text, r"^    mode:", "mode in default skeleton")

    print("create_task_skeleton.py preflight default-disabled contract holds")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
