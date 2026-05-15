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

import hashlib
import pathlib
import re
import subprocess
import sys
import tempfile


SCRIPT_DIR = pathlib.Path(__file__).resolve().parent
GENERATOR = SCRIPT_DIR / "create_task_skeleton.py"


def repo_key_for(cwd: pathlib.Path) -> str:
    absolute = cwd.resolve(strict=False)
    try:
        absolute = absolute.resolve(strict=True)
    except FileNotFoundError:
        pass
    base = re.sub(r"[^A-Za-z0-9._-]+", "-", absolute.name).strip("-._") or "repo"
    return f"{base}-{hashlib.sha256(str(absolute).encode('utf-8')).hexdigest()[:8]}"


def generate_skeleton(tmpdir: pathlib.Path, *extra_args: str, root: pathlib.Path | None = None) -> str:
    output_dir = tmpdir / "draft"
    output_dir.mkdir(parents=True, exist_ok=True)
    fake_cwd = tmpdir / "repo"
    fake_cwd.mkdir(parents=True, exist_ok=True)
    command = [
        sys.executable,
        str(GENERATOR),
        "demo-skeleton-preflight",
        "--cwd",
        str(fake_cwd),
        "--output-dir",
        str(output_dir),
    ]
    if root is not None:
        command.extend(["--root", str(root)])
    command.extend(extra_args)
    proc = subprocess.run(
        command,
        check=True,
        capture_output=True,
        text=True,
    )
    task_path = pathlib.Path(proc.stdout.strip())
    return task_path.read_text(encoding="utf-8")


def write_environment_default(root: pathlib.Path, cwd: pathlib.Path, cli: str, *, inline_executor: bool = False) -> None:
    env_dir = root / "profiles" / repo_key_for(cwd)
    env_dir.mkdir(parents=True, exist_ok=True)
    executor_lines = [f"executor: {{default_cli: {cli}}}"] if inline_executor else ["executor:", f'  default_cli: "{cli}"']
    (env_dir / "environment.yaml").write_text(
        "\n".join(
            [
                'id: "test"',
                f'cwd: "{cwd}"',
                "commands: {}",
                *executor_lines,
                "constraints:",
                '  network: "approval_required"',
                '  secrets_policy: "never_read_env_files"',
                '  destructive_commands: "deny"',
                "",
            ]
        ),
        encoding="utf-8",
    )


def generated_executor_cli(yaml_text: str) -> str:
    match = re.search(r"^executor:\n(?:  .+\n)*?  cli: ([A-Za-z0-9_-]+)$", yaml_text, re.MULTILINE)
    if not match:
        raise SystemExit("regression: generated skeleton is missing executor.cli")
    return match.group(1)


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
        tmpdir = pathlib.Path(tmp)
        yaml_text = generate_skeleton(tmpdir)

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
    if generated_executor_cli(yaml_text) != "codex":
        raise SystemExit("regression: unset environment executor default should generate executor.cli: codex")

    with tempfile.TemporaryDirectory() as tmp:
        tmpdir = pathlib.Path(tmp)
        fake_cwd = tmpdir / "repo"
        fake_cwd.mkdir(parents=True, exist_ok=True)
        root = tmpdir / "galley"
        write_environment_default(root, fake_cwd, "claude")
        yaml_text = generate_skeleton(tmpdir, root=root)
    if generated_executor_cli(yaml_text) != "claude":
        raise SystemExit("regression: environment executor default should generate executor.cli: claude")

    with tempfile.TemporaryDirectory() as tmp:
        tmpdir = pathlib.Path(tmp)
        fake_cwd = tmpdir / "repo"
        fake_cwd.mkdir(parents=True, exist_ok=True)
        root = tmpdir / "galley"
        write_environment_default(root, fake_cwd, "claude", inline_executor=True)
        yaml_text = generate_skeleton(tmpdir, root=root)
    if generated_executor_cli(yaml_text) != "claude":
        raise SystemExit("regression: inline environment executor default should generate executor.cli: claude")

    with tempfile.TemporaryDirectory() as tmp:
        tmpdir = pathlib.Path(tmp)
        fake_cwd = tmpdir / "repo"
        fake_cwd.mkdir(parents=True, exist_ok=True)
        root = tmpdir / "galley"
        write_environment_default(root, fake_cwd, "claude")
        yaml_text = generate_skeleton(tmpdir, "--executor-cli", "codex", root=root)
    if generated_executor_cli(yaml_text) != "codex":
        raise SystemExit("regression: explicit --executor-cli should override environment default")

    print("create_task_skeleton.py preflight and executor default contracts hold")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
